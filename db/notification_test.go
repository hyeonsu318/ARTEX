package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// 이 파일의 테스트는 모두 실제 PostgreSQL에 연결한다(DB가 없으면 건너뜀). 여기 쓰인
// SQL은 FOR UPDATE SKIP LOCKED, make_interval, JSONB, 여러 행 IN(...) 플레이스홀더
// 조합 등 "컴파일은 되지만 런타임에 오류가 날 수 있는" 작성법이라, 실제로 돌려봐야만
// 검증된 것으로 친다.

func notifyTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newTestChannel은 채널을 하나 만들고, 테스트가 끝나면 자동으로 삭제한다.
func newTestChannel(t *testing.T, d *DB, kind, mode string, filter string) *NotificationChannel {
	t.Helper()
	if filter == "" {
		filter = `{}`
	}
	ch := &NotificationChannel{
		Name:       "테스트 채널-" + t.Name(),
		Kind:       kind,
		Mode:       mode,
		Config:     json.RawMessage(`{"webhook":"https://example.com/hook"}`),
		Filter:     json.RawMessage(filter),
		RatePerMin: 100,
	}
	id, err := d.SaveNotificationChannel(context.Background(), ch)
	if err != nil {
		t.Fatalf("채널 생성 실패: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	ch.ID = id
	return ch
}

// addTestEvent는 이벤트 하나를 직접 기록한다(finding을 거치지 않음). 분배와 전달 테스트에 사용.
func addTestEvent(t *testing.T, d *DB, kind string, findingID int64, snap notify.Snapshot) int64 {
	t.Helper()
	snap.Kind = kind
	snap.FindingID = findingID
	id, err := d.AddNotificationEvent(context.Background(), kind, findingID, snap)
	if err != nil {
		t.Fatalf("이벤트 기록 실패: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE id=$1`, id) })
	return id
}

func TestNotificationAssetNamesResolvesAndPreservesOrder(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// 세 유형의 자산은 각자 다른 표시 기준을 쓴다: 도메인, IP, URL.
	insertAsset := func(query, value string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRow(query, value).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	domID := insertAsset(`INSERT INTO assets(type, domain) VALUES('subdomain',$1) RETURNING id`, "a.example.com")
	ipID := insertAsset(`INSERT INTO assets(type, ip) VALUES('ip',$1) RETURNING id`, "10.1.2.3")
	svcID := insertAsset(`INSERT INTO assets(type, url) VALUES('service',$1) RETURNING id`, "https://a.example.com/admin")
	t.Cleanup(func() {
		d.Exec(`DELETE FROM assets WHERE id IN ($1,$2,$3)`, domID, ipID, svcID)
	})

	// 입력 순서를 의도적으로 섞고, 존재하지 않는 id도 하나 포함한다.
	got, err := d.NotificationAssetNames(ctx, []int64{svcID, 999999999, domID, ipID, svcID})
	if err != nil {
		t.Fatalf("자산 이름 해석 실패: %v", err)
	}
	want := []string{"https://a.example.com/admin", "a.example.com", "10.1.2.3"}
	if len(got) != len(want) {
		t.Fatalf("자산 이름 개수 불일치, 기대값 %v 실제값 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("순서/값 불일치, 기대값 %v 실제값 %v", want, got)
		}
	}
}

// TestRecordNotificationEventTxUnwindsOnFailure는 세이브포인트 메커니즘의 핵심
// 테스트다: 트랜잭션 안에서 notification_events 쓰기가 반드시 실패하게 만들어 놓고
// (항상 false인 제약을 임시로 추가), ① 함수가 false를 보고하는지 ② 트랜잭션이
// aborted 상태로 들어가지 않아 이후 구문이 계속 실행되는지를 단언한다.
//
// 세이브포인트가 없다면, PostgreSQL은 트랜잭션 전체를 무효화하고 이후 모든 구문이
// "current transaction is aborted"로 실패한다 — 그것이 바로 "알림 테이블 문제가
// 취약점을 DB에 저장하지 못하게 하는" 장애 경로다.
//
// 여기서는 의도적으로 **COMMIT이 아니라 ROLLBACK으로 마무리**한다: ALTER TABLE은
// PG에서 트랜잭션 범위에 들어가므로, 한 번 커밋되면 그 임시 제약이 schema에 영구히
// 남아 이후의 모든 테스트를 함께 망가뜨린다. 롤백하면 DDL이 자동으로 취소되어 수동
// 정리가 필요 없다. 단언은 "트랜잭션이 아직 살아 있는가"만 필요하며, 실제로 커밋할
// 필요는 없다.
func TestRecordNotificationEventTxUnwindsOnFailure(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// 방어적 정리: 이전 실행이 이 제약을 남겨뒀다면 먼저 제거한다.
	if _, err := d.Exec(`ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notify_test_never`); err != nil {
		t.Fatal(err)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // 임시 제약을 되돌림, 함수 주석 참고

	// NOT VALID: 이후에 쓰여지는 행에만 제약을 걸고, DB에 이미 있는 기존 이벤트는
	// 검증하지 않는다(그러지 않으면 기존 행이 위반해서 제약을 추가할 수 없다).
	if _, err := tx.ExecContext(ctx, `ALTER TABLE notification_events ADD CONSTRAINT notify_test_never CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("임시 제약 추가 실패: %v", err)
	}
	if RecordNotificationEventTx(ctx, tx, notify.EventFindingCreated, 1, notify.Snapshot{Severity: "high"}) {
		t.Fatal("반드시 실패하는 제약 아래에서도 쓰기 성공으로 보고됨")
	}
	// 핵심 단언: 트랜잭션이 여전히 사용 가능하다.
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("트랜잭션이 오염됨(세이브포인트가 적용되지 않음): %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("롤백 실패: %v", err)
	}
	// DDL이 롤백과 함께 취소됐는지 확인하여, 이후 테스트에 지뢰를 남기지 않는다.
	var exists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='notify_test_never')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("임시 제약이 롤백으로 취소되지 않아 이후 테스트를 오염시킴")
	}
}

func TestFanOutRoutesEventsByFilter(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	all := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	onlyCritical := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"min_severity":"critical"}`)
	sqlOnly := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["SQL"]}`)

	highSQL := addTestEvent(t, d, notify.EventFindingCreated, 1001, notify.Snapshot{Severity: "high", VulnClass: "SQL 인젝션"})
	lowXSS := addTestEvent(t, d, notify.EventFindingCreated, 1002, notify.Snapshot{Severity: "low", VulnClass: "XSS"})
	criticalXSS := addTestEvent(t, d, notify.EventFindingCreated, 1003, notify.Snapshot{Severity: "critical", VulnClass: "XSS"})

	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatalf("분배 실패: %v", err)
	}

	cases := []struct {
		name    string
		eventID int64
		channel int64
		want    bool
	}{
		{"전체 수신 채널이 high를 받음", highSQL, all.ID, true},
		{"전체 수신 채널이 low를 받음", lowXSS, all.ID, true},
		{"심각도 전용 채널이 high를 건너뜀", highSQL, onlyCritical.ID, false},
		{"심각도 전용 채널이 critical을 받음", criticalXSS, onlyCritical.ID, true},
		{"SQL 전용 채널이 SQL을 받음", highSQL, sqlOnly.ID, true},
		{"SQL 전용 채널이 XSS를 건너뜀", lowXSS, sqlOnly.ID, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exists bool
			if err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE event_id=$1 AND channel_id=$2)`,
				tc.eventID, tc.channel).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists != tc.want {
				t.Fatalf("전달 존재 여부: 기대값 %v 실제값 %v", tc.want, exists)
			}
		})
	}

	// 다시 분배해도 중복 전달이 생기면 안 된다(fanned_out 멱등성).
	events, deliveries, err := d.FanOutPendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if events != 0 || deliveries != 0 {
		t.Fatalf("이미 분배된 이벤트는 다시 처리되면 안 되는데, events=%d deliveries=%d", events, deliveries)
	}
}

// TestFanOutMarksEventsWithNoMatchingChannel은 "이벤트가 어떤 채널에도 매칭되지 않음"
// 상황을 다룬다. 이런 이벤트도 똑같이 분배 완료로 표시되어야 한다. 그러지 않으면
// 분배 대기 집합에 영원히 남아 매 tick마다 다시 스캔된다.
func TestFanOutMarksEventsWithNoMatchingChannel(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	pick := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["절대 매칭되지 않는 유형"]}`)
	_ = pick

	ev := addTestEvent(t, d, notify.EventFindingCreated, 2001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	_, deliveries, err := d.FanOutPendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("전달이 생기면 안 되는데 %d개 생김", deliveries)
	}
	var fanned bool
	if err := d.QueryRowContext(ctx, `SELECT fanned_out FROM notification_events WHERE id=$1`, ev).Scan(&fanned); err != nil {
		t.Fatal(err)
	}
	if !fanned {
		t.Fatal("매칭되는 채널이 없는 이벤트도 분배 완료로 표시되어야 한다. 그러지 않으면 무한히 재스캔된다")
	}
}

func TestClaimRealtimeDeliveriesHonorsLeaseAndMode(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	realtime := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	digest := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)

	addTestEvent(t, d, notify.EventFindingCreated, 3001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// 실시간 수령은 realtime 채널의 그 한 건만 가져와야 하고, digest 채널 건은 건드리면 안 된다.
	got, err := d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err != nil {
		t.Fatalf("수령 실패: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("1건을 수령해야 하는데 %d건 수령됨", len(got))
	}
	if got[0].State != NotifyStateSending || got[0].Attempts != 1 {
		t.Fatalf("수령 후에는 sending이고 attempts=1이어야 하는데, state=%s attempts=%d", got[0].State, got[0].Attempts)
	}
	// 조인으로 불러온 렌더링 컨텍스트는 전부 갖춰져 있어야 한다(채널 설정 + 이벤트 스냅샷 + finding id).
	if got[0].Channel == nil || len(got[0].Channel.Config) == 0 {
		t.Fatal("수령 결과에 채널 설정이 없어 렌더링이 실패함")
	}
	if got[0].FindingID != 3001 {
		t.Fatalf("finding id가 이벤트에서 전달되지 않음, 실제값 %d", got[0].FindingID)
	}

	// 리스가 아직 만료되지 않았으므로, 두 번째 수령은 비어 있어야 한다 — 이것이
	// "같은 행이 두 dispatcher에 동시에 전달되지 않음"을 보장한다.
	again, err := d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("리스 기간 안에는 중복 수령되면 안 되는데 %d건 수령됨", len(again))
	}

	// digest 채널의 전달은 실시간 수령에 걸리면 안 된다.
	left, err := d.ClaimRealtimeDeliveries(ctx, digest.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("실시간 수령이 digest 채널의 전달을 가져오면 안 되는데 %d건 가져옴", len(left))
	}
}

// TestClaimExpiredLeaseRecovers는 크래시 자가 치유를 다룬다: 프로세스가 전달 중
// 죽으면 sending 상태의 행이 남는데, 리스가 만료된 뒤에는 반드시 다시 수령할 수
// 있어야 한다. 그러지 않으면 이 전달은 영원히 멈춰 있게 된다.
func TestClaimExpiredLeaseRecovers(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 4001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("첫 수령 실패: %v (%d건)", err, len(first))
	}
	// 리스를 수동으로 과거로 밀어, "리스가 만료됨"을 흉내낸다.
	if _, err := d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE id=$1`, first[0].ID); err != nil {
		t.Fatal(err)
	}
	second, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("리스가 만료된 sending 행은 다시 수령할 수 있어야 하는데 %d건 수령됨", len(second))
	}
	if second[0].Attempts != 2 {
		t.Fatalf("재수령은 시도 횟수를 누적해야 하는데 %d임", second[0].Attempts)
	}
}

func TestClaimSkipsDisabledChannel(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 5001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// 비활성화하면 기존 발송 대기 전달도 함께 skipped로 표시된다.
	if err := d.SetNotificationChannelEnabled(ctx, ch.ID, false); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := d.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStateSkipped {
		t.Fatalf("비활성화된 채널의 기존 발송 대기 전달은 skipped로 표시되어야 하는데 %s임", state)
	}
	got, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("비활성화된 채널은 수령되면 안 되는데 %d건 수령됨", len(got))
	}
}

func TestDigestBatchDueAndStableBatchID(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i := 0; i < 3; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(6000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// 배치가 막 만들어져 나이가 0이므로, 30분 주기에서는 기한이 되면 안 된다.
	due, err := d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err != nil {
		t.Fatalf("배치 기한 판단 실패: %v", err)
	}
	if due {
		t.Fatal("방금 만든 배치가 즉시 기한이 되면 안 된다")
	}

	// 세 전달의 생성 시각을 한꺼번에 과거로 밀어, 주기를 다 채운 배치를 흉내낸다.
	if _, err := d.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '40 minutes' WHERE channel_id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	due, err = d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("주기를 넘긴 배치는 기한이 된 것으로 판정되어야 한다")
	}

	batch, err := d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err != nil {
		t.Fatalf("다이제스트 배치 수령 실패: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("다이제스트는 3건 전부를 한 번에 가져가야 하는데 %d건 가져옴", len(batch))
	}
	if batch[0].BatchID == nil {
		t.Fatal("다이제스트 배치는 반드시 batch_id를 기록해야 한다. 그러지 않으면 이력에서 이 건들이 함께 발송된 것인지 알 수 없다")
	}
	firstBatchID := *batch[0].BatchID
	for _, dl := range batch {
		if dl.BatchID == nil || *dl.BatchID != firstBatchID {
			t.Fatalf("같은 배치는 batch_id를 공유해야 하는데 %v vs %d", dl.BatchID, firstBatchID)
		}
	}

	// 이 배치를 **전체적으로** 실패 재배치한 뒤 다시 수령해도 batch_id는 원래
	// 값을 유지해야 한다(COALESCE의 역할): 그러지 않으면 재시도 한 번으로
	// "이 배치는 함께 발송된 것"이라는 사실이 지워진다.
	//
	// 한 건만이 아니라 배치 전체를 재배치해야 한다 — 전달 엔진이 다이제스트
	// 메시지를 보낼 때도 이렇게 처리한다(메시지 하나가 배치 전체를 대표하며,
	// 성패를 함께한다). 한 건만 재배치하면 나머지는 여전히 리스 기간 안에 있어서,
	// 재수령은 당연히 그 한 건만 가져오게 된다.
	allIDs := make([]int64, 0, len(batch))
	for _, dl := range batch {
		allIDs = append(allIDs, dl.ID)
	}
	if err := d.RescheduleDeliveries(ctx, allIDs, time.Second, "실패 시뮬레이션"); err != nil {
		t.Fatal(err)
	}
	// 리스를 과거로 밀어, 백오프 시간이 지난 것을 흉내낸다.
	if _, err := d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 3 {
		t.Fatalf("재수령은 3건 전부를 가져와야 하는데 %d건 가져옴", len(reclaimed))
	}
	if reclaimed[0].BatchID == nil || *reclaimed[0].BatchID != firstBatchID {
		t.Fatalf("재시도 후 batch_id는 원래 값 %d를 유지해야 하는데 %v임", firstBatchID, reclaimed[0].BatchID)
	}
}

func TestDeliveryStateTransitions(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 7001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	got, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("수령 실패: %v (%d)", err, len(got))
	}
	id := got[0].ID

	if err := d.RescheduleDeliveries(ctx, []int64{id}, time.Second, "네트워크 흔들림"); err != nil {
		t.Fatal(err)
	}
	var state, lastErr string
	if err := d.QueryRow(`SELECT state, last_error FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStatePending || lastErr != "네트워크 흔들림" {
		t.Fatalf("재배치 후에는 pending이고 이유가 기록되어야 하는데 state=%s err=%q", state, lastErr)
	}

	if err := d.FailDeliveries(ctx, []int64{id}, "재시도 소진"); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT state FROM notification_deliveries WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStateFailed {
		t.Fatalf("failed여야 하는데 %s임", state)
	}

	// 수동 재발송은 재시도 횟수를 초기화하고 즉시 만료시켜야 한다. 그러지 않으면 예전 실패 예산을 물려받는다.
	if err := d.RetryNotificationDelivery(ctx, id); err != nil {
		t.Fatalf("재발송 실패: %v", err)
	}
	var attempts int
	var next time.Time
	if err := d.QueryRow(`SELECT state, attempts, next_attempt_at FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStatePending || attempts != 0 {
		t.Fatalf("재발송 후에는 pending이고 attempts=0이어야 하는데 state=%s attempts=%d", state, attempts)
	}
	if next.After(time.Now().Add(time.Second)) {
		t.Fatal("재발송은 즉시 수령 가능해야 한다")
	}

	// 이미 전달된 건은 재발송할 수 없어야 한다.
	if err := d.MarkDeliveriesSent(ctx, []int64{id}); err != nil {
		t.Fatal(err)
	}
	if err := d.RetryNotificationDelivery(ctx, id); err == nil {
		t.Fatal("이미 전달된 건은 재발송이 허용되면 안 된다")
	}
}

func TestListNotificationDeliveriesPagingAndFilter(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	for i := 0; i < 5; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(8000+i), notify.Snapshot{Severity: "high", Name: "페이지네이션 테스트"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute); err != nil {
		t.Fatal(err)
	}

	page1, total, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 1, 2)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if total != 5 {
		t.Fatalf("총수는 5여야 하는데 %d", total)
	}
	if len(page1) != 2 {
		t.Fatalf("페이지당 2건이어야 하는데 %d", len(page1))
	}
	// 최신이 앞에: 첫 페이지의 첫 건 id가 두 번째 페이지의 첫 건보다 커야 한다.
	page2, _, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID >= page1[0].ID {
		t.Fatalf("페이지 순서는 최신이 앞이어야 하는데 page1[0]=%d page2[0]=%d", page1[0].ID, page2[0].ID)
	}
	// 렌더링 컨텍스트는 이력과 함께 반환되어야 한다. 그러지 않으면 목록에서 "무엇을 보냈는지" 보여줄 수 없다.
	if page1[0].ChannelName == "" || page1[0].FindingID == 0 {
		t.Fatalf("이력 항목에 표시용 필드가 없음: %+v", page1[0])
	}

	// 상태로 필터링: pending은 없어야 한다.
	pending, totalPending, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStatePending}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if totalPending != 0 || len(pending) != 0 {
		t.Fatalf("pending 전달이 없어야 하는데 %d건 있음 (total=%d)", len(pending), totalPending)
	}
}

func TestSetFindingStatusWithNotifyOnlyEmitsOnRealChange(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	tk, err := d.CreateTask("알림 상태 변경 테스트", "목표", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)
	f, err := es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQL 인젝션", Name: "상태 변경 테스트 케이스",
		Severity: "high", Summary: "요약",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// 저장 시 이미 finding_created 이벤트 한 건이 기록되므로, 먼저 그 수를 세어 기준선으로 삼는다.
	var base int
	if err := d.QueryRow(`SELECT count(*) FROM notification_events WHERE finding_id=$1`, f.FindingID).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if base < 1 {
		t.Fatal("취약점 저장은 같은 트랜잭션 안에서 푸시 이벤트 하나를 기록해야 한다")
	}

	// 같은 상태로 바꾸기: 이벤트가 생기면 안 된다(중복 제출이 푸시 잡음을 내지 않도록).
	from, found, notified, err := d.SetFindingStatusWithNotify(ctx, f.FindingID, "pending")
	if err != nil || !found {
		t.Fatalf("상태 설정 실패: found=%v err=%v", found, err)
	}
	if notified {
		t.Fatal("상태가 바뀌지 않았을 때는 푸시 이벤트가 기록되면 안 된다")
	}
	if from != "pending" {
		t.Fatalf("변경 전 상태 pending을 반환해야 하는데 %q", from)
	}

	// 실제 변경: 이벤트가 기록되고 from/to가 저장되어야 한다.
	from, found, notified, err = d.SetFindingStatusWithNotify(ctx, f.FindingID, "fixed")
	if err != nil || !found {
		t.Fatalf("상태 설정 실패: found=%v err=%v", found, err)
	}
	if !notified {
		t.Fatal("상태가 실제로 바뀌었을 때는 푸시 이벤트가 기록되어야 한다")
	}
	if from != "pending" {
		t.Fatalf("from은 pending이어야 하는데 %q", from)
	}
	var snapshot []byte
	if err := d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot); err != nil {
		t.Fatalf("상태 변경 이벤트를 찾지 못함: %v", err)
	}
	var snap notify.Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FromStatus != "pending" || snap.ToStatus != "fixed" {
		t.Fatalf("스냅샷의 상태 전환이 잘못됨: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// 스냅샷에는 렌더링에 필요한 필드가 담겨야 한다. 그러지 않으면 상태 변경 메시지가 빈 껍데기가 된다.
	if snap.VulnClass != "SQL 인젝션" || snap.Severity != "high" || snap.Name != "상태 변경 테스트 케이스" {
		t.Fatalf("스냅샷에 렌더링 필드가 없음: %+v", snap)
	}
	var status string
	if err := d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "fixed" {
		t.Fatalf("상태가 fixed로 업데이트되어야 하는데 %s", status)
	}

	// 존재하지 않는 취약점: found=false이며 오류는 없어야 한다.
	if _, found, _, err := d.SetFindingStatusWithNotify(ctx, 999999999, "fixed"); err != nil || found {
		t.Fatalf("존재하지 않는 취약점은 found=false이고 오류가 없어야 하는데 found=%v err=%v", found, err)
	}
}

func TestNotificationStatsSnapshot(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 9001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	stats, err := d.NotificationStatsSnapshot(ctx)
	if err != nil {
		t.Fatalf("통계 실패: %v", err)
	}
	if stats.Channels < 1 || stats.ChannelsOn < 1 {
		t.Fatalf("채널 집계가 잘못됨: %+v", stats)
	}
	if stats.Pending < 1 {
		t.Fatalf("발송 대기 전달이 집계되어야 함: %+v", stats)
	}
	// 방금 만든 전달의 적체 나이는 0에 가까워야 하며, 음수나 큰 값이면 안 된다.
	if stats.BacklogAgeMS < 0 || stats.BacklogAgeMS > int64(time.Hour/time.Millisecond) {
		t.Fatalf("적체 나이가 유효하지 않음: %d ms", stats.BacklogAgeMS)
	}
	_ = ch
}

func TestNotificationChannelCRUDRoundTrip(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	ch := &NotificationChannel{
		Name:       "CRUD 왕복",
		Kind:       notify.KindEmail,
		Mode:       NotifyModeDigest,
		Config:     json.RawMessage(`{"host":"smtp.example.com","port":587,"from":"a@b.c","to":["x@y.z"]}`),
		Filter:     json.RawMessage(`{"min_severity":"medium","on_status_change":true}`),
		RatePerMin: 42,
	}
	id, err := d.SaveNotificationChannel(ctx, ch)
	if err != nil {
		t.Fatalf("신규 생성 실패: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })

	got, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if got.Mode != NotifyModeDigest || got.RatePerMin != 42 || got.Name != "CRUD 왕복" {
		t.Fatalf("왕복 필드가 일치하지 않음: %+v", got)
	}
	if !got.IsEnabled() {
		t.Fatal("기본값은 활성화여야 한다")
	}
	var cfg map[string]any
	if err := json.Unmarshal(got.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["host"] != "smtp.example.com" {
		t.Fatalf("설정이 올바르게 저장되지 않음: %v", cfg)
	}
	var filter notify.Filter
	if err := json.Unmarshal(got.Filter, &filter); err != nil {
		t.Fatal(err)
	}
	if filter.MinSeverity != "medium" || !filter.OnStatusChange {
		t.Fatalf("필터 조건이 올바르게 저장되지 않음: %+v", filter)
	}

	// 업데이트 후 다시 읽기.
	got.Name = "이름 변경됨"
	off := false
	got.Enabled = &off
	if _, err := d.SaveNotificationChannel(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "이름 변경됨" || after.IsEnabled() {
		t.Fatalf("업데이트가 반영되지 않음: %+v", after)
	}

	// 삭제 후에는 "존재하지 않음"을 보고해야 하며 조용히 성공하면 안 된다.
	if err := d.DeleteNotificationChannel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.NotificationChannelByID(ctx, id); err != ErrNotificationChannelNotFound {
		t.Fatalf("ErrNotificationChannelNotFound를 기대했는데 %v", err)
	}
	if err := d.DeleteNotificationChannel(ctx, id); err != ErrNotificationChannelNotFound {
		t.Fatalf("중복 삭제는 존재하지 않음을 보고해야 하는데 %v", err)
	}
}

// TestSaveNotificationChannelKeepsExplicitZeroRate는 예전에 잘못 작성됐던 부분을
// 고정해 둔다: **0은 유효한 설정이며 "속도 제한 없음"을 뜻하므로, db 계층이 이를
// "지정하지 않음"으로 취급해 기본값으로 덮어써서는 안 된다**.
//
// 과거 버그: SaveNotificationChannel에 `if RatePerMin <= 0 { 기본값 사용 }`이라고
// 쓰여 있어서, 문서, UI 안내, takeTokens는 모두 "0=속도 제한 없음"으로 해석하는데,
// 저장 계층만 몰래 20(DingTalk/WeCom/Telegram)이나 100(Feishu)으로 바꿔버렸다 —
// 운영자는 제한을 풀었다고 생각했지만 실제로는 막혀 있었고 아무 안내도 없었다.
// "지정하지 않음"과 "명시적으로 0"의 차이는 요청 본문만이 표현할 수 있으므로,
// 기본값은 server 계층에서 채우고(notifyCreateChannel 참고) db 계층은 저장만 한다.
func TestSaveNotificationChannelKeepsExplicitZeroRate(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// 명시적으로 0(속도 제한 없음): 그대로 저장되어야 한다.
	unlimited := &NotificationChannel{
		Name: "속도 제한 없음", Kind: notify.KindDingTalk, RatePerMin: 0,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	id, err := d.SaveNotificationChannel(ctx, unlimited)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	got, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.RatePerMin != 0 {
		t.Fatalf("명시적 0은 속도 제한 없음을 뜻하므로 그대로 저장되어야 하는데 %d", got.RatePerMin)
	}
	if got.Mode != NotifyModeRealtime {
		t.Fatalf("기본 모드는 realtime이어야 하는데 %s", got.Mode)
	}

	// 음수는 유효하지 않은 입력이므로, 몰래 다른 값으로 바뀌는 게 아니라 거부되어야 한다.
	bad := &NotificationChannel{
		Name: "음수 속도 제한", Kind: notify.KindDingTalk, RatePerMin: -1,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	if _, err := d.SaveNotificationChannel(ctx, bad); err == nil {
		t.Fatal("음수 속도 제한은 거부되어야 한다")
	}
}

// TestDeleteChannelCascadesDeliveries는 외래키 동작을 고정해 둔다: 채널을 삭제하면
// 전달 이력도 함께 사라지지만(설정이 없어지면 이력을 해석할 방법이 없다), 이벤트
// 자체는 남아 있어야 한다 — 다른 채널이 아직 참조하고 있을 수 있기 때문이다.
func TestDeleteChannelCascadesDeliveries(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	ev := addTestEvent(t, d, notify.EventFindingCreated, 9101, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("전제 조건이 성립하지 않음: 전달이 생성되지 않음")
	}
	if err := d.DeleteNotificationChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("채널 삭제 후 그 전달은 CASCADE로 삭제되어야 하는데 아직 %d건 남음", after)
	}
	var evExists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM notification_events WHERE id=$1)`, ev).Scan(&evExists); err != nil {
		t.Fatal(err)
	}
	if !evExists {
		t.Fatal("채널 삭제가 이벤트 자체까지 함께 삭제하면 안 된다")
	}
}

// TestClaimDigestBatchHonorsCallerLimit는 감사에서 지적된 틈 하나를 다룬다:
// 다이제스트 채널은 예전에 토큰 버킷을 완전히 건너뛰었다 — allow는 takeTokens가
// 차감했지만 아무도 쓰지 않아서, rate_per_min이 digest 모드에는 전혀 작동하지
// 않았다. 지금은 limit도 제약에 참여한다.
func TestClaimDigestBatchHonorsCallerLimit(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i := 0; i < 10; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(7000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// limit=3으로 수령: 3건만 가져오고 나머지는 DB에 남아야 한다.
	got, err := d.ClaimDigestBatch(ctx, ch.ID, 3, time.Minute)
	if err != nil {
		t.Fatalf("수령 실패: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("호출자의 속도 제한 할당량대로 3건만 수령해야 하는데 %d건", len(got))
	}
	// limit=0은 이번 라운드 할당량이 소진됐음을 뜻한다: 한 건도 수령하면 안 되고, 오류도 나면 안 된다.
	if got, err := d.ClaimDigestBatch(ctx, ch.ID, 0, time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("할당량이 0일 때는 0건 수령하고 오류도 없어야 하는데 %d건 err=%v", len(got), err)
	}
}

// TestFinishFindingRetestEmitsStatusChange는 감사에서 지적된 무결성 공백 하나를
// 다룬다: 재테스트 결론이 "수정됨"일 때 상태는 실제로 바뀌지만, 그 UPDATE는 DB에
// 직접 쓰는 것이라 알림이 포함된 버전을 건너뛴다 — 그래서 on_status_change를
// 설정한 채널은 이런 상태 전환을 전혀 받지 못하고, 화면에서 상태가 조용히 바뀌어
// 운영자가 플랫폼을 열어봐야만 알 수 있었다.
//
// 이 테스트는 "상태를 바꾸는 모든 경로가 상태 변경 이벤트를 기록해야 한다"를 고정해 둔다.
func TestFinishFindingRetestEmitsStatusChange(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	tk, err := d.CreateTask("재테스트 푸시 테스트", "목표", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)
	f, err := es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQL 인젝션", Name: "재테스트 대상",
		Severity: "high", Summary: "요약",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// 재테스트 기록 하나를 만들어 바로 완료 상태로 밀어넣는다.
	rt, _, _, err := d.CreateFindingRetest(ctx, f.FindingID, "재검토")
	if err != nil {
		t.Fatal(err)
	}
	if rt.ConversationID == nil {
		t.Fatal("재테스트는 세션 하나와 연결되어야 한다")
	}
	// 재테스트는 결론을 저장하기 전에 먼저 running 상태여야 한다(실제 흐름과 동일).
	if ok, err := d.StartFindingRetest(ctx, rt.ID); err != nil || !ok {
		t.Fatalf("재테스트 시작 실패: ok=%v err=%v", ok, err)
	}
	if err := d.RecordFindingRetestResult(ctx, *rt.ConversationID, "fixed", "수정됨", "증거"); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishFindingRetest(rt.ID, "completed", ""); err != nil {
		t.Fatalf("재테스트 종료 실패: %v", err)
	}

	var status string
	if err := d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != FindingFixed {
		t.Fatalf("재테스트가 수정됨으로 판정한 뒤 상태는 fixed여야 하는데 %s", status)
	}

	// 핵심 단언: 상태 변경 이벤트가 하나 있어야 하고, from/to가 올바라야 한다.
	var snapshot []byte
	err = d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2 ORDER BY id DESC LIMIT 1`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot)
	if err != nil {
		t.Fatalf("재테스트가 수정됨으로 판정하면 상태 변경 푸시 이벤트가 기록되어야 한다(그러지 않으면 on_status_change를 설정한 채널이 받지 못함): %v", err)
	}
	var snap notify.Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FromStatus != "pending" || snap.ToStatus != FindingFixed {
		t.Fatalf("스냅샷의 상태 전환이 잘못됨: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// 스냅샷에는 렌더링에 필요한 필드가 담겨야 한다. 그러지 않으면 푸시가 빈 껍데기로 나간다.
	if snap.Name != "재테스트 대상" || snap.Severity != "high" {
		t.Fatalf("스냅샷에 렌더링 필드가 없음: %+v", snap)
	}
}
