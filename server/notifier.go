package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// 전역 설정 키 (settings 키-값 테이블에 저장되므로 테이블 생성이 필요 없음).
const (
	// settingNotifyEnabled 는 푸시 마스터 스위치다. 기본값은 켬: 점검 기간에 한 번에 차단하는 용도이지,
	// 기능의 활성화 조건이 아니다 — 진짜 활성화 조건은 「채널을 설정했는가」이다.
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL 은 취약점 상세 백링크를 생성하는 외부 접속 주소다
	// (예: https://artex.example.com). 비워 두면 메시지에 백링크 버튼이 포함되지 않는다.
	// 프로젝트에 재사용할 수 있는 외부 주소 설정이 없어 여기에 하나 추가했다.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes 는 요약 모드의 주기(분)다.
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick 은 전달 엔진의 폴링 간격이다. 3초가 이 엔진 실시간성의 상한이며,
	// 「취약점 DB 저장」에서 「메시지가 IM 에 도착」하기까지의 주된 지연 요인이기도 하다.
	notifyTick = 3 * time.Second
	// notifyLease 는 전달 건을 가져올 때의 리스 시간이다. 단일 전달의 최악 소요 시간보다 훨씬 커야 하며
	// (notify 패키지 HTTP 클라이언트 타임아웃 15초), 그렇지 않으면 같은 행이 두 개의
	// dispatcher 에 의해 동시에 전달된다.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick 은 라운드당 분배할 이벤트 수를 제한하여 채널을 처음 활성화할 때
	// 과거에 쌓인 것을 한꺼번에 전부 전달 작업으로 펼치는 것을 방지한다.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes 는 요약 주기의 기본값이다.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick 은 채널에 속도 제한이 없을 때의 라운드당 전달 상한이다.
	// 존재 이유는 「채널을 무제한으로 설정 + 한 번의 스캔에서 수천 건의 취약점 발견」이
	// 단일 라운드 루프를 오래 막히게 만드는 것을 방지하는 것이다.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick 은 단일 채널이 라운드당 최대 몇 건까지 전달하는지다.
	//
	// 이 상한은 **리스 시간**에서 역산한다: 가져올 때 행에 거는 것이 리스(notifyLease = 3분)이며,
	// 한 라운드에서 직렬로 전달하는 건수가 많아 최악의 소요 시간이 리스를 넘으면 마지막 몇 건을 보내기 전에 리스가 만료된다.
	// 단일 프로세스에서는 상관없지만(Run 은 하나의 goroutine 이 직렬로 실행하며 tick 은 재진입하지 않음), **두 개의
	// 프로세스가 같은 DB 에 연결**되면 상대편이 리스가 만료된 행을 다시 가져가 중복 전송하며,
	// attempts 도 이중으로 증가하고 원래 프로세스가 아직 전달 중인데도 실패로 판정한다.
	//
	// 값: 3분 리스 / 30초 단건 타임아웃 = 6 은 **리스를 정확히 다 쓰는** 여유 없는 값이라
	// 쓸 수 없다. 5 를 쓰면 최악 소요 시간 150초에 30초의 여유가 남는다. 이 관계는
	// TestNotifyTickBudgetFitsWithinLease 가 고정한다 — notifyLease,
	// notifySendTimeout 또는 이 값 중 어느 하나를 바꿔도 그 단언이 실패한다.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout 은 단일 전달의 타임아웃이다. 이는 위 상수의 값도 함께 결정하며,
	// 둘을 곱한 값이 notifyLease 를 넘으면 안 된다. TestNotifyTickBudgetFitsWithinLease 참고.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff 는 실패 재시도의 백오프 시퀀스이며, 인덱스는 이미 시도한 횟수다.
// 3번의 기회(첫 시도 포함)는 db.MaxNotifyAttempts 와 대응하며, 둘을 반드시 함께 바꿔야 한다.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier 는 취약점 푸시의 전달 엔진이다.
//
// Scheduler 와 나란히 독립 goroutine 으로 실행된다(server.New 참고). 의도적으로
// Scheduler 의 tick 을 재사용하지 않는다: 푸시의 실시간성 요구(3초)가 트리거의 업무 리듬과 다르고,
// 둘의 실패가 서로 연루되지 않아야 한다 — 푸시가 멈춰도 agent 트리거에 영향을 주면 안 된다.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu 는 buckets 를 보호한다. 채널 수가 적고 경합이 낮아 뮤텍스 하나로 충분하며,
	// 더 세분화된 구조를 도입할 가치가 없다.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket 은 단일 채널의 토큰 버킷이다.
//
// 「분당 카운트 후 0 으로 초기화」하는 슬라이딩 윈도우 대신 토큰 버킷을 쓰는 이유는 후자의 경계 효과가 나쁘기 때문이다:
// 윈도우 끝에서 20건을 채워 보내고 바로 다음 순간 20건을 또 보내면 플랫폼 입장에서는 1초에 40건이라,
// 속도 제한에 걸린다. 토큰 버킷은 일정한 속도로 보충되므로 이런 버스트를 자연스럽게 피한다.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run 은 ctx 가 끝날 때까지 루프한다. server.New 가 한 번 시작한다.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step 은 한 라운드를 실행한다: 먼저 새 이벤트를 분배하고, 이어서 기한이 된 작업을 전달한다.
//
// 어느 단계가 실패해도 로그만 남기고 루프를 중단하지 않는다 — 알림 시스템의 장애가 프로세스 수준 문제로 번져서는 안 된다.
// 각 tick 은 독립적이므로 다음 라운드에서 자연스럽게 재시도된다.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] 이벤트 분배 실패: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] 채널 읽기 실패: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// 토큰 버킷의 계량 단위는 **메시지 건수**(HTTP 요청 수와 동일)이며 취약점 건수가 아니다.
		// 실시간 모드에서는 둘이 같다(취약점 하나에 메시지 하나). 요약 모드에서는 취약점 한 배치를 합쳐
		// 메시지 하나로 만들므로 토큰을 하나만 소모한다.
		//
		// 두 모드 모두 먼저 토큰 버킷에 묻고 그 한도만큼 가져온다 — 순서를 바꾸면 안 되며, 바꾸면 속도 제한에 막힌
		// 전달 건이 이미 재시도 횟수를 소모해 버린다.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan 은 요약 채널의 이번 라운드 토큰 소모량과 배치 크기 상한을 반환한다.
//
// 두 반환값은 **서로 다른 단위**이며, 이것이 함수로 분리한 이유다:
//
//   - tokens 는 메시지 건수다. 취약점 한 배치를 메시지 하나로 합쳐 HTTP 요청을 한 번 보내므로 항상 1 이다.
//     따라서 rate_per_min 은 여전히 digest 에 적용된다(분당 최대 이만큼의 요약 메시지).
//   - claimLimit 는 이번 배치에 최대 몇 건의 취약점을 담는지다. 메모리 상한에만 제약되며 요청 예산과는 무관하다.
//
// 예전에는 rate_per_min 이 digest 에 적용되게 하려고 라운드당 요청 예산을
// (notifyMaxSendsPerChannelPerTick, 리스에서 역산한 값) 그대로 배치 크기로 넘겼다.
// 그 결과 rate_per_min=20 인 채널은 3초 tick 에서 토큰을 1개만 보충받아 요약
// 메시지마다 취약점이 1개뿐이었다 — digest 가 「요약 문구가 붙은 실시간 푸시」로 퇴화하여, 독자는
// 「최근 30분간 취약점 1개 추가」가 줄줄이 오는 것을 받았고 db.MaxDigestBatchSize 에는 영원히 도달하지 못했다.
//
// 이 증상은 종단 간 테스트에서 발견하기 어렵다(기존 케이스는 모두 충분히 큰 limit 을 수동으로
// stepDigest 에 넘겨 step 안의 한도 계산을 우회한다). 그래서 결정을 여기에 모아
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget 가 직접 고정한다.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime 은 특정 채널의 실시간 작업을 가져와 전달한다. 취약점 하나에 메시지 하나.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] 실시간 전달 가져오기 실패 channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("채널 유형 %q 이(가) 등록되지 않았습니다", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// 렌더링 실패는 로컬 데이터 문제이므로 재시도해도 나아지지 않는다.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest 는 배치 기한이 되면 특정 채널의 대기 중인 전달 건을 하나의 메시지로 모아 보낸다.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] 요약 배치 판단 실패 channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] 요약 배치 가져오기 실패 channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("채널 유형 %q 이(가) 등록되지 않았습니다", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// 스냅샷이 손상되어 메시지에 들어가지 못한 전달 건은 명시적으로 실패 처리해야 한다. 그렇게 하지 않으면 그것들은
	// included 밖에 남아 메시지에도 실패 목록에도 들어가지 않으며, 전송 성공 시 그 상태가
	// 이후의 일괄 표시에서 누락되어 리스가 만료될 때까지 영원히 sending 에 머물며 반복해서 가져가진다.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "이벤트 스냅샷을 해석할 수 없어 이 취약점을 메시지로 렌더링할 수 없습니다"
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] 손상된 스냅샷 전달 건 표시 실패 channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] 스냅샷을 해석할 수 없는 전달 %d 건 건너뜀 channel=%d", len(skipped), ch.ID)
	}
	// 메시지에 들어간 것만 send 에 넘긴다: included[i] 와 msg.Items[i] 는 엄격히 대응하며,
	// send 는 이 대응 관계에 의존하여 「채널이 앞 K 건이 담겼다고 보고함」을 올바른 전달 행에 반영한다.
	n.send(ctx, channel, cfg, msg, included)
}

// send 는 전달하고 결과에 따라 상태를 전이시킨다.
//
// 같은 배치의 전달 건(요약 모드에서는 수십 건일 수 있음)은 하나의 전송 결과를 공유한다: 전달되거나 배치 전체가 재시도된다.
// 건별 재시도는 하지 않는다 — 요약 메시지는 하나이므로 일부만 재전송하면 배치 의미가 뒤틀린다.
//
// 유일한 예외는 **채널 길이 상한으로 인한 분할**이다: 채널이 실제로 앞 K 건만 담았다고 보고하면,
// K+1 번째부터는 다음 배치로 남겨야 하며 함께 성공으로 표시되어서는 안 된다. 그렇지 않으면 잘려 나간
// 취약점들이 메시지에도 실패 목록에도 없이 완전히 사라진다.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// 단일 전달에 상한을 두어, 한 채널이 멈춰 이번 라운드의 나머지 채널이 모두 막히는 것을 방지한다.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// 채널이 보고한 건수는 전달 건수를 넘을 수 없다. 실제로 발생했다면 렌더링 계층이 잘못 계산한 것이므로,
			// 전부 전달된 것으로 처리하고 문제를 기록해 두는 편이 기록을 엉망으로 만드는 것보다 낫다.
			log.Printf("[notify] 채널이 보고한 전달 건수 %d 이(가) 전달 건수 %d 를 초과함 channel=%s, 전부 전달된 것으로 처리",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] 전달 완료 표시 실패 channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// 이 메시지가 채널 길이 상한에 도달했다: 남은 것은 즉시 큐로 되돌려 다음 tick 에서 이어서 보낸다.
			// RescheduleDeliveries 가 아닌 DeferDeliveries 를 사용한다 — 이것은 실패가 아니므로
			// 재시도 예산을 소모해서는 안 된다(가져올 때 이미 낙관적으로 +1 했으므로 거기서 다시 뺀다).
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("이 메시지가 채널 길이 상한에 도달하여 앞 %d 건만 전달되었으며 나머지는 다음 배치로 남깁니다", delivered)); err != nil {
				log.Printf("[notify] 분할 이어 보내기 큐잉 실패 channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// 채널이 오류도 내지 않고 몇 건이 전달되었는지도 말하지 않았다. 실패로 처리하여(백오프 적용),
		// 이 전달 건이 반복해서 가져가지기만 하고 영원히 표시되지 않는 일을 막는다.
		err = fmt.Errorf("채널이 전달 건수를 보고하지 않았습니다(delivered=%d)", delivered)
	}

	// 실패 처리는 배치 전체의 최대 시도 횟수로 판단하지 않고 **건별로** 결정한다.
	//
	// 예전에는 `if maxAttempts(deliveries) >= MaxNotifyAttempts` 로 배치 전체를 실패 처리했으나 배치 안의
	// 각 건의 시도 횟수는 서로 다르다: 이미 두 번 재시도한 오래된 전달 건(attempts=2)이 같은 배치의
	// 새 전달 건(attempts=1)까지 함께 failed 로 끌고 들어간다 — 새 취약점이 재시도를 한 번도 못 해보고 영구히 사라져,
	// 「오래된 행이 새 행을 끌어들이지 않게 한다」는 취지와 정반대다.
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] 실패 상태 표시 오류 channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("%d 회 재시도 후에도 실패: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] 실패 상태 표시 오류 channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// 지연별로 그룹화하여 재배치한다: 백오프는 3단계뿐이라 그룹 수가 자연히 작으므로 건마다 따로
	// UPDATE 를 보낼 필요가 없다(그러면 500건 배치에서 500번의 왕복이 생긴다).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] 전달 재배치 실패 channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] 전달 실패 channel=%d kind=%s 영구 실패=%d 재시도 소진=%d 재시도 대기=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries 는 all 중 keep 에 없는 것들을 반환한다(포인터 동일성으로 비교).
// 「메시지에 들어가지 못한」 전달 건을 찾는 데 쓴다 — 이들은 명시적으로 처리해야 하며 회색지대에 남겨 두어서는 안 된다.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt 는 채널 구현을 가져와 그 설정을 해석한다.
// ok=false 를 반환하면 유형이 등록되지 않은 것이며, 전달 건은 무한 재시도하지 않고 바로 실패로 판정해야 한다.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// 설정 해석에 실패하면 빈 map 을 준다: 채널 자체의 Validate 가 「어느 필드가 빠졌는지」를 알려 주며,
		// 그 오류가 JSON 파싱 오류보다 사용자가 고치는 데 더 도움이 된다.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle 은 단일 취약점 메시지를 렌더링한다.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch 는 요약 메시지를 렌더링한다. 스냅샷을 건별로 해석한다 — 하나가 손상되면 그것만 건너뛰며,
// 배치 요약 전체가 망가지지 않게 한다.
//
// 반환값 included 와 msg.Items 는 **엄격히 일대일 대응**한다(i 번째 전달 건 ↔ i 번째 항목).
// 이 대응 관계는 필수 요건이다: 호출자는 「채널이 앞 K 건이 담겼다고 보고함」에 따라 앞 K 개의 전달 건을
// 전달 완료로 표시한다. 여기서 손상된 스냅샷을 건너뛰면서 건너뛴 전달 건을 included 에서 제거하지 않으면,
// 인덱스가 어긋난다 — 실패해야 할 손상 항목이 전달 완료로 표시되고, 정상 항목은 미전달로 오판된다.
// 손상된 항목은 호출자가 명시적으로 실패로 표시한다. stepDigest 참고.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// 손상된 스냅샷은 메시지에도 included 에도 들어가지 않는다 — 처리는 호출자가 맡는다
			// (「전달 완료」에 섞어 슬쩍 넘기지 않고 명시적으로 실패로 표시).
			log.Printf("[notify] 요약 배치에서 해석할 수 없는 스냅샷을 건너뜀 delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("요약 배치 %d 건의 전달 건을 모두 해석할 수 없습니다", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor 는 이벤트 스냅샷을 푸시할 항목으로 렌더링하며, 자산 이름과 상세 백링크도 함께 해석한다.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// 자산 이름 해석 실패가 푸시를 막아서는 안 된다: 이름을 읽지 못하는 것이 알림을 못 받는 것보다 훨씬 가볍고,
		// 메시지에서 자산 한 줄이 빠질 뿐이다.
		log.Printf("[notify] 자산 이름 해석 실패 finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// 상세 페이지 라우트는 web/src/app/(main)/function/findings/detail/page.tsx 참고,
		// 이 페이지는 query 파라미터 id 에서 취약점 id 를 읽는다.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens 는 채널 토큰 버킷에서 **최대 want 개**의 토큰을 가져가고 실제로 가져온 개수를 반환한다.
//
// 토큰 하나 = 메시지 하나(HTTP 요청 한 번). 실시간 모드에서는 호출자가 필요한 만큼 전달하고,
// 요약 모드에서는 취약점 한 배치가 메시지 하나만 보내므로 1 을 전달한다.
//
// 버킷 용량은 해당 채널의 분당 상한이며 일정한 속도로 보충된다. ratePerMin<=0 은 무제한을 뜻하며,
// 유한하지만 충분히 큰 값을 반환하여 단일 라운드 루프가 무한히 쌓인 것에 붙잡히지 않게 한다.
//
// want 상한은 필수다: 이것이 없으면 버킷을 통째로 비울 수밖에 없는데 호출자에게는 자체 라운드 상한이 있어,
// 더 가져간 토큰은 쓰이지도 못하고 다음 보충 전에 허공으로 사라진다 — 모아 둔 버스트 용량에는 영원히 도달할 수 없고,
// 「이번 라운드에 대기 중인 전달 건이 하나도 없는」 경우에도 그대로 차감된다.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// 경과한 실제 시간만큼 보충하며 속도는 초당 ratePerMin/60 이다.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// 아주 작은 epsilon 을 더한 뒤 정수로 만든다: 토큰 수는 부동소수점으로 누적되므로 두 번에 나눠 채울 때
	// 0.5 + 0.5 가 0.9999999999 가 될 수 있고 곧바로 int() 하면 0 으로 잘려 —
	// 수학적으로는 가득 찬 버킷에서 토큰을 꺼내지 못하게 된다. 1e-9 는 토큰 하나보다 훨씬 작아 진짜 부족분을 놓치지 않는다.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled 는 마스터 스위치를 읽는다.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL 은 백링크용 외부 주소를 반환하며 끝의 슬래시를 제거한다.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval 은 요약 주기를 반환하며, 유효하지 않거나 설정되지 않았으면 기본값으로 되돌린다.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot 은 전달 건에 대응하는 이벤트의 스냅샷을 해석한다.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("전달 %d 의 이벤트 스냅샷이 비어 있습니다", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("전달 %d 의 이벤트 스냅샷 파싱 실패:%w", dl.ID, err)
	}
	if snap.Kind == "" {
		// 이벤트 유형은 이벤트 행을 기준으로 하며 스냅샷 안의 값은 이전 버전이 기록했을 수 있다.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
