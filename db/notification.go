package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// 이 파일은 IM 푸시의 채널 설정과 이벤트 계층이다. 전달(delivery) 작업의 수령과 상태
// 전환은 db/notification_delivery.go를 참고.
//
// 두 가지 불변식이 있으며, 이 파일을 수정할 때 반드시 지켜야 한다:
//
//  1. 취약점을 쓰는 트랜잭션(RecordFindingTx)은 InsertNotificationEventTx를 호출해
//     한 번 맹목적으로 INSERT만 한다 — 알림 관련 테이블을 읽지도, 필터 매칭도 하지
//     않는다. 여기서 읽기 작업을 도입하면 사용자가 잘못 설정한 필터 조건 때문에
//     취약점 쓰기 트랜잭션이 오염되거나 중단될 수 있다.
//  2. 필터 매칭은 절대 오류를 내지 않는다: 설정이 잘못되어 있으면 무조건 "매칭됨"으로
//     처리한다(notify.Match 참고). 과하게 보내는 쪽을 택하며, 빠뜨리지 않는다.

// ErrNotificationChannelNotFound: 채널이 존재하지 않음.
var ErrNotificationChannelNotFound = errors.New("알림 채널이 존재하지 않습니다")

// 전달 상태.
const (
	NotifyStatePending = "pending" // 발송 대기
	NotifyStateSending = "sending" // 어떤 dispatcher가 수령했고, 리스가 아직 만료되지 않음
	NotifyStateSent    = "sent"    // 전달 완료
	NotifyStateFailed  = "failed"  // 재시도 소진 또는 영구 실패, 수동 재발송 가능
	NotifyStateSkipped = "skipped" // 채널이 비활성화되어 더 이상 발송하지 않음
)

// 푸시 모드.
const (
	NotifyModeRealtime = "realtime"
	NotifyModeDigest   = "digest"
)

// ValidNotifyMode는 푸시 모드를 화이트리스트로 검증한다(findings.status와 같은 이유로
// DB CHECK를 쓰지 않아, 이후 확장이 쉽다).
func ValidNotifyMode(m string) bool {
	return m == NotifyModeRealtime || m == NotifyModeDigest
}

// NotificationChannel은 채널 인스턴스 설정 하나다. Config와 Filter는 원본 JSON 그대로
// 유지하며, 해석은 notify 패키지에 맡긴다 — db 계층은 그 필드들의 의미를 모른다.
type NotificationChannel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Mode   string          `json:"mode"`
	Config json.RawMessage `json:"config"`
	Filter json.RawMessage `json:"filter"`
	// Enabled를 포인터로 두는 이유는 "이 필드를 전달하지 않음"과 "명시적으로 false를
	// 전달함"을 구분하기 위해서다 — 프런트엔드의 스위치 컨트롤은 변경된 필드만 제출한다.
	Enabled    *bool     `json:"enabled,omitempty"`
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// IsEnabled는 채널 활성화 여부를 반환한다; Enabled가 nil(로드되지 않음)이면 활성화된 것으로 처리한다.
func (c *NotificationChannel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// NotificationEvent는 이벤트 사실(fact) 한 건이다.
type NotificationEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	FindingID int64           `json:"finding_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
}

const notificationChannelCols = `id, name, kind, enabled, config, mode, filter, rate_per_min, created_at, updated_at`

func scanNotificationChannel(sc interface{ Scan(...any) error }) (*NotificationChannel, error) {
	var c NotificationChannel
	var enabled bool
	if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &enabled, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = &enabled
	return &c, nil
}

// ListNotificationChannels는 모든 채널 인스턴스를 반환한다. 활성화된 것이 앞에 오고,
// 동급은 id순이다. 정렬을 SQL에 둔 이유는 UI와 dispatcher가 같은 안정적인 순서를
// 보게 하기 위함이다.
func (d *DB) ListNotificationChannels(ctx context.Context) ([]*NotificationChannel, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels
ORDER BY enabled DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		c, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotificationChannelByID는 채널 하나를 가져온다.
func (d *DB) NotificationChannelByID(ctx context.Context, id int64) (*NotificationChannel, error) {
	row := d.QueryRowContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=$1`, id)
	c, err := scanNotificationChannel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotificationChannelNotFound
	}
	return c, err
}

// SaveNotificationChannel은 채널을 새로 만들거나 업데이트한다.
//
// 업데이트 시에는 호출자가 명시적으로 준 필드(non-nil/non-empty)만 덮어쓴다. 이렇게
// 하면 프런트엔드가 부분 수정 폼을 제출할 때, 화면에 표시하지 않는 config 필드까지
// 되돌려 보낼 필요가 없다 — 되돌려 보내면 오히려 "마스킹된 값이 실제 키를 덮어쓰는"
// 사고로 이어진다.
func (d *DB) SaveNotificationChannel(ctx context.Context, c *NotificationChannel) (int64, error) {
	if c.Mode == "" {
		c.Mode = NotifyModeRealtime
	}
	// 여기서는 의도적으로 0을 **전혀** 가공하지 않는다: 0은 유효한 설정이며 "속도 제한 없음"을 뜻한다.
	//
	// 예전에는 `if c.RatePerMin <= 0 { c.RatePerMin = 기본값 }`이라고 썼는데, 의도는
	// "지정하지 않았을 때 안전한 기본값을 준다"였지만, 그러면 "명시적으로 0을 지정함"도
	// 함께 삼켜버렸다 — 문서, UI 안내, takeTokens 모두 0을 "속도 제한 없음"으로 해석하는데
	// 여기만 몰래 20(DingTalk/WeCom/Telegram)이나 100(Feishu)으로 바꿔버려서, 운영자는
	// 제한을 풀었다고 생각했지만 실제로는 분당 20건에 막혀 있었고 아무런 안내도 없었다.
	//
	// "지정하지 않음"과 "명시적으로 0"의 차이는 호출자만 알 수 있으므로(요청 본문에서
	// 필드가 생략된 것 vs 명시적으로 0을 전달한 것), 기본값은 필드가 생략됐을 때 server
	// 계층에서 채운다. notifyCreateChannel 참고.
	if c.RatePerMin < 0 {
		return 0, errors.New("속도 제한 값은 음수일 수 없습니다")
	}
	if c.Config == nil {
		c.Config = json.RawMessage(`{}`)
	}
	if c.Filter == nil {
		c.Filter = json.RawMessage(`{}`)
	}
	enabled := c.IsEnabled()

	if c.ID == 0 {
		var id int64
		err := d.QueryRowContext(ctx, `INSERT INTO notification_channels(name,kind,enabled,config,mode,filter,rate_per_min)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin).Scan(&id)
		return id, err
	}
	res, err := d.ExecContext(ctx, `UPDATE notification_channels
SET name=$2, kind=$3, enabled=$4, config=$5, mode=$6, filter=$7, rate_per_min=$8
WHERE id=$1`,
		c.ID, c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotificationChannelNotFound
	}
	return c.ID, nil
}

// SetNotificationChannelEnabled는 켜고 끄는 것을 전환한다.
//
// 채널을 비활성화할 때, 아직 발송되지 않은 전달 건도 함께 skipped로 표시한다: 그러지
// 않으면 다시 활성화했을 때 "비활성화 기간 동안 쌓인" 오래된 취약점 알림이 한꺼번에
// 도착하게 되는데, 이미 시의성을 잃었고 신규 발견으로 오인되기 쉽다.
func (d *DB) SetNotificationChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notification_channels SET enabled=$2 WHERE id=$1`, id, enabled)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotificationChannelNotFound
		}
		if !enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET state=$2, last_error=$3
WHERE channel_id=$1 AND state IN ($4,$5)`,
				id, NotifyStateSkipped, "채널이 비활성화됨", NotifyStatePending, NotifyStateSending); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteNotificationChannel은 채널을 삭제한다. 전달 이력은 외래키 CASCADE로 함께
// 삭제된다(채널 설정 자체가 없어지면 이력을 해석할 방법이 없다).
func (d *DB) DeleteNotificationChannel(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationChannelNotFound
	}
	return nil
}

// RecordNotificationEventTx는 호출자의 트랜잭션 안에서 **최선을 다해** 푸시 이벤트 한
// 건을 기록한다.
//
// 이것이 취약점 쓰기 경로에서 유일한 알림 관련 작업이다: INSERT 한 번뿐이며, 어떤
// 테이블도 읽지 않고 채널도 모르고 필터도 돌리지 않는다. 트랜잭션이 커밋되면 "취약점
// 저장"과 "푸시 작업 존재"가 원자적으로 일관됨이 보장되며, 커밋은 성공했는데 큐에는
// 들어가지 않아 메시지가 영구히 유실되는 구간이 존재하지 않는다.
//
// 두 가지 핵심 설계가 있으며, 둘 다 즉흥적으로 쓴 게 아니다:
//
//  1. **왜 SAVEPOINT를 쓰는가**: PostgreSQL에서는 트랜잭션 안의 어떤 구문이든 오류가
//     나면 트랜잭션 전체가 aborted 상태가 되고, 이후 모든 구문(COMMIT 포함)이 전부
//     실패한다. 그래서 "이 INSERT의 오류는 무시하고 호출자가 계속 커밋하게 한다"는
//     PG에서는 세이브포인트로 오류를 그 구문 하나에 격리하지 않으면 불가능하다.
//     세이브포인트가 없으면 "전체 롤백"만 남는 유일한 선택이 된다.
//
//  2. **왜 전체 롤백이 잘못된 선택인가**: 푸시는 편의 기능이고, 취약점 기록이 바로
//     제품 본체다. 알림 테이블의 문제(마이그레이션 안 된 예전 DB, 일시적 디스크 장애)
//     때문에 고위험 취약점이 저장되지 못해서는 안 된다. 그래서 여기서는 오류를 격리하고
//     로그를 남기고 false를 반환하여, 취약점 쓰기는 평소대로 커밋되게 한다 — 대가는 이
//     한 건의 푸시를 잃는 것뿐이다. bool을 반환하고 error를 반환하지 않는 것은 의도적인
//     선택이다: 호출자는 이것을 쓰기 성패에 영향을 주는 오류로 취급해서는 안 된다.
func RecordNotificationEventTx(ctx context.Context, tx *sql.Tx, kind string, findingID int64, snap notify.Snapshot) bool {
	raw, err := json.Marshal(snap)
	if err != nil {
		log.Printf("[notify] 푸시 이벤트 직렬화 실패 finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT notify_event`); err != nil {
		log.Printf("[notify] 세이브포인트 생성 실패 finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3)`,
		kind, findingID, string(raw)); err != nil {
		log.Printf("[notify] 푸시 이벤트 기록 실패 finding=%d(취약점 기록에는 영향 없음): %v", findingID, err)
		// 세이브포인트로 롤백하여 트랜잭션을 aborted 상태에서 구해낸다.
		if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notify_event`); rbErr != nil {
			log.Printf("[notify] 세이브포인트 롤백 실패 finding=%d: %v", findingID, rbErr)
		}
		return false
	}
	// 세이브포인트를 해제하여, 긴 트랜잭션 안에 쓸모없는 세이브포인트가 쌓이지 않게 한다.
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT notify_event`)
	return true
}

// AddNotificationEvent는 InsertNotificationEventTx의 독립 트랜잭션 버전으로, 기존
// 트랜잭션 밖의 호출 지점(예: 채널의 "테스트 메시지 보내기" — 실제 finding이 없음)에서 사용한다.
func (d *DB) AddNotificationEvent(ctx context.Context, kind string, findingID int64, snap notify.Snapshot) (int64, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return 0, fmt.Errorf("알림 이벤트 스냅샷 직렬화 실패: %w", err)
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3) RETURNING id`,
		kind, findingID, string(raw)).Scan(&id)
	return id, err
}

// FanOutPendingEvents는 아직 분배되지 않은 취약점 이벤트를 현재 활성화된 채널별로
// 펼쳐 전달 작업으로 만들고, 이번 라운드에서 처리한 이벤트 수와 새로 만든 전달 수를
// 반환한다.
//
// 라운드 전체가 하나의 트랜잭션 안에서 이루어진다: 이벤트는 FOR UPDATE SKIP LOCKED로
// 수령하므로, 여러 프로세스가 동시에 돌아도 각자 다른 행을 가져간다(이 프로젝트의
// 보관 큐 수령도 같은 기법을 쓴다. db/task_archives.go의 completeNextArchiveJob 참고).
//
// 필터 매칭은 SQL이 아니라 의도적으로 Go 쪽에서 한다: 채널의 필터 조건은 선택적
// 필드들의 JSONB 묶음이라, SQL로 여섯 가지 조합의 매칭을 표현하면 쿼리를 유지보수하기
// 어려워진다. 반면 채널 수는 "사람이 수동으로 설정한 몇 개" 수준이므로, 전부 로드한
// 뒤 메모리에서 하나씩 비교하는 쪽이 더 빠르고 테스트하기도 쉽다.
//
// 어떤 채널에도 매칭되지 않은 이벤트도 똑같이 fanned_out으로 표시한다 — 그러지 않으면
// 분배 대기 집합에 영원히 남아, 매 tick마다 다시 스캔되기 때문이다.
func (d *DB) FanOutPendingEvents(ctx context.Context, limit int) (eventCount, deliveryCount int, err error) {
	if limit <= 0 {
		limit = 200
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // 커밋이 성공하면 no-op

	channels, err := listEnabledNotificationChannelsTx(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, finding_id, snapshot FROM notification_events
WHERE NOT fanned_out ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, 0, err
	}
	var (
		events      []NotificationEvent
		parsedSnaps []notify.Snapshot
	)
	for rows.Next() {
		var ev NotificationEvent
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.FindingID, &ev.Snapshot); err != nil {
			rows.Close()
			return 0, 0, err
		}
		var snap notify.Snapshot
		// 스냅샷은 우리가 직접 쓴 것이라 이론상 반드시 해석 가능하다; 해석이 실패해도
		// 전달 흐름을 막지는 않지만, 이 이벤트는 필드가 전부 비어 있어서 필터 조건이
		// 있는 모든 채널에서 건너뛰어지게 된다 — 한 건을 덜 보내는 쪽을 택하며, 잘못된
		// 행 하나가 전체 큐를 막아버리게 하지 않는다.
		_ = json.Unmarshal(ev.Snapshot, &snap)
		// kind는 행의 값을 기준으로 한다: 스냅샷 안의 것은 렌더링용 복사본이라, 예전
		// 버전이 써놓은 값일 수 있다.
		snap.Kind = ev.Kind
		events = append(events, ev)
		parsedSnaps = append(parsedSnaps, snap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(events) == 0 {
		return 0, 0, tx.Commit()
	}

	type pending struct {
		eventID   int64
		channelID int64
	}
	var toInsert []pending
	for i, snap := range parsedSnaps {
		for _, ch := range channels {
			if !notify.Match(notify.ParseFilter(ch.Filter), snap) {
				continue
			}
			toInsert = append(toInsert, pending{eventID: events[i].ID, channelID: ch.ID})
		}
	}
	if len(toInsert) > 0 {
		var (
			vals []string
			args []any
		)
		for _, p := range toInsert {
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
			args = append(args, p.eventID, p.channelID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(event_id,channel_id) VALUES `+strings.Join(vals, ","), args...); err != nil {
			return 0, 0, err
		}
	}

	// 이번 라운드의 이벤트를 분배 완료로 표시한다. 어떤 채널에도 매칭되지 않은 이벤트도 함께 표시한다(함수 주석 참고).
	ids := make([]string, 0, len(events))
	markArgs := make([]any, 0, len(events))
	for _, ev := range events {
		markArgs = append(markArgs, ev.ID)
		ids = append(ids, fmt.Sprintf("$%d", len(markArgs)))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_events SET fanned_out=true WHERE id IN (`+strings.Join(ids, ",")+`)`, markArgs...); err != nil {
		return 0, 0, err
	}
	return len(events), len(toInsert), tx.Commit()
}

// listEnabledNotificationChannelsTx는 트랜잭션 안에서 활성화된 채널을 가져온다. 수가
// 매우 적어서 페이지네이션도 캐시도 하지 않는다 — 캐시를 두면 "설정을 바꾼 게 언제
// 반영되는가"라는 추가적인 타이밍 문제가 생긴다.
func listEnabledNotificationChannelsTx(ctx context.Context, tx *sql.Tx) ([]*NotificationChannel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, config, mode, filter, rate_per_min
FROM notification_channels WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// NotificationAssetNames는 자산 id를 짧은 표시용 이름으로 해석하여 푸시 메시지에 쓴다.
//
// 반환 순서는 입력 인자와 같고, 길이는 입력보다 작을 수 있다(존재하지 않는 id는
// 건너뜀). 입력 순서를 유지하는 이유는 같은 취약점의 메시지가 여러 번 전달돼도
// 자산 순서가 안정적이게 하기 위함이다 — 그러지 않으면 재시도 후 받은 메시지에서
// 자산 순서가 바뀌어, "자산이 바뀌었다"로 잘못 읽힐 수 있다.
func (d *DB) NotificationAssetNames(ctx context.Context, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args := placeholders(1, ids)
	rows, err := d.QueryContext(ctx, `SELECT id, type, domain, ip, url, app_name, bundle_id FROM assets WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := map[int64]string{}
	for rows.Next() {
		var (
			id                int64
			typ               string
			domain, ip, url   sql.NullString
			appName, bundleID sql.NullString
		)
		if err := rows.Scan(&id, &typ, &domain, &ip, &url, &appName, &bundleID); err != nil {
			return nil, err
		}
		labels[id] = assetDisplayName(typ, domain.String, ip.String, url.String, appName.String, bundleID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if label, ok := labels[id]; ok && label != "" {
			out = append(out, label)
		}
	}
	return out, nil
}

// assetDisplayName은 자산 유형에 따라 가장 식별력 있는 표시명을 고른다.
// 마지막에는 빈 문자열을 반환하여, "이름을 가져올 수 없는 자산"을 어떻게 보여줄지는
// 호출자가 정하게 한다 — 이 함수는 플레이스홀더를 지어내지 않는다. 그러지 않으면
// "자산#42" 같은 잡음이 푸시 메시지에 섞여 들어가, 읽는 사람이 실제 도메인으로
// 착각할 수 있다.
func assetDisplayName(typ, domain, ip, url, appName, bundleID string) string {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	switch typ {
	case "root_domain", "subdomain":
		return domain
	case "ip":
		return ip
	case "app":
		return pick(appName, bundleID)
	case "service", "endpoint":
		return pick(url, domain, ip)
	default:
		return pick(domain, ip, url, appName)
	}
}

// SetFindingStatusWithNotify는 취약점 처리 상태를 업데이트하고, 같은 트랜잭션 안에서
// 상태 변경 푸시 이벤트 하나를 기록한다.
//
// 반환값: from=변경 전 상태; found=취약점 존재 여부; notified=이벤트 기록 성공 여부.
//
// 의도적인 동작 세 가지:
//   - 상태가 실제로 바뀌지 않았으면 이벤트를 기록하지 않는다. 프런트엔드 드로어가 같은
//     값을 중복 제출하거나 자동화 스크립트가 멱등 재생해도 푸시 잡음이 생기면 안 된다.
//   - 취약점이 존재하지 않으면 found=false를 반환하고 아무 것도 쓰지 않으며, 호출자가
//     이를 404로 변환한다.
//   - 이벤트 기록 실패는 상태 업데이트에 영향을 주지 않으므로(RecordNotificationEventTx의
//     세이브포인트 설명 참고) notified=false일 때도 상태는 이미 성공적으로 바뀌어 있다.
//     호출자는 이를 오류로 취급해서는 안 된다.
func (d *DB) SetFindingStatusWithNotify(ctx context.Context, id int64, status string) (from string, found bool, notified bool, err error) {
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		from, found, _, notified, txErr = SetFindingStatusTx(ctx, tx, id, status)
		return txErr
	})
	return from, found, notified, err
}

// SetFindingStatusTx는 **호출자의 트랜잭션** 안에서 취약점 상태를 업데이트하고 상태
// 변경 푸시 이벤트를 기록한다.
//
// 트랜잭션 수준 함수로 뽑아낸 이유는 상태를 바꾸는 모든 경로가 같은 의미 체계를
// 공유하게 하기 위함이다 — 이전에는 patchFinding만 알림이 포함된 버전을 탔고,
// **재테스트 결론이 "수정됨"일 때**(finding_retests의 그 `UPDATE findings SET
// status=...`)는 DB에 직접 썼다. 그래서 `on_status_change`를 설정한 채널은 이런
// 유형의 상태 전환을 전혀 받지 못했다: 화면에서 상태가 조용히 바뀌고, 운영자는
// 플랫폼을 열어봐야만 알 수 있었다.
//
// 반환값: from=변경 전 상태, found=취약점 존재 여부, changed=상태가 실제로 바뀌었는지
// 여부, notified=이벤트 기록 성공 여부(기록 실패는 상태 업데이트에 영향을 주지 않음.
// RecordNotificationEventTx 참고).
func SetFindingStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) (from string, found bool, changed bool, notified bool, err error) {
	var (
		vulnclass, name, severity, summary string
		taskID                             sql.NullInt64
		assetIDs                           []byte
	)
	scanErr := tx.QueryRowContext(ctx, `SELECT vulnclass, name, severity, summary, task_id, asset_ids, status
FROM findings WHERE id=$1 FOR UPDATE`, id).
		Scan(&vulnclass, &name, &severity, &summary, &taskID, &assetIDs, &from)
	if scanErr == sql.ErrNoRows {
		return "", false, false, false, nil
	}
	if scanErr != nil {
		return "", false, false, false, scanErr
	}
	found = true
	if from == status {
		// 상태가 실제로 바뀌지 않았으면 이벤트를 기록하지 않는다: 같은 값을 중복
		// 제출하거나 멱등 재생해도 푸시 잡음이 생기면 안 된다.
		return from, true, false, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET status=$2 WHERE id=$1`, id, status); err != nil {
		return from, true, false, false, err
	}
	var assets []int64
	_ = json.Unmarshal(assetIDs, &assets)
	notified = RecordNotificationEventTx(ctx, tx, notify.EventFindingStatusChanged, id, notify.Snapshot{
		Kind:       notify.EventFindingStatusChanged,
		FindingID:  id,
		TaskID:     taskID.Int64,
		VulnClass:  vulnclass,
		Name:       name,
		Severity:   severity,
		Summary:    summary,
		AssetIDs:   assets,
		FromStatus: from,
		ToStatus:   status,
	})
	return from, true, true, notified, nil
}

// NotificationStats는 알림 페이지 상단의 개요 집계다.
type NotificationStats struct {
	Channels     int   `json:"channels"`
	ChannelsOn   int   `json:"channels_on"`
	Pending      int   `json:"pending"`
	Failed       int   `json:"failed"`
	SentToday    int   `json:"sent_today"`
	BacklogAgeMS int64 `json:"backlog_age_ms"` // 가장 오래된 발송 대기 전달 건의 경과 밀리초
}

// NotificationStatsSnapshot은 알림 시스템의 건강도를 집계한다.
// BacklogAgeMS는 "푸시가 멈춰 있는가"를 가장 직접적으로 보여주는 지표다 — pending
// 건수보다 훨씬 유용한데, 쌓인 게 3건이라 해도 그 3건이 3초 전인지 3시간 전인지는
// 완전히 다른 이야기이기 때문이다.
func (d *DB) NotificationStatsSnapshot(ctx context.Context) (*NotificationStats, error) {
	var s NotificationStats
	if err := d.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM notification_channels),
    (SELECT count(*) FROM notification_channels WHERE enabled),
    (SELECT count(*) FROM notification_deliveries WHERE state IN ($1,$2)),
    (SELECT count(*) FROM notification_deliveries WHERE state=$3),
    (SELECT count(*) FROM notification_deliveries WHERE state=$4 AND sent_at >= date_trunc('day', now())),
    COALESCE((SELECT EXTRACT(EPOCH FROM (now() - min(created_at))) * 1000 FROM notification_deliveries WHERE state=$1), 0)::bigint`,
		NotifyStatePending, NotifyStateSending, NotifyStateFailed, NotifyStateSent).
		Scan(&s.Channels, &s.ChannelsOn, &s.Pending, &s.Failed, &s.SentToday, &s.BacklogAgeMS); err != nil {
		return nil, err
	}
	return &s, nil
}
