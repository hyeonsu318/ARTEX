package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 이 파일은 전달 작업의 수령과 상태 전환을 다룬다.
//
// 수령에는 긴 트랜잭션이 아니라 "리스(lease)"를 쓴다: 행을 sending으로 바꾸고
// next_attempt_at을 미래로 미뤄 리스 만료 시각으로 삼은 뒤, 트랜잭션을 커밋하고
// 나서야 실제 네트워크 전달을 한다. 이렇게 하면 전달하는 동안 DB 락을 들고 있지
// 않는다 — 네트워크 요청은 몇 초가 걸릴 수 있는데(클라이언트 타임아웃 15초), 행
// 락을 쥐고 있으면 같은 DB의 다른 쓰기 작업을 끌어내리게 된다.
//
// 대가는 프로세스가 전달 도중 죽으면 행이 sending에 멈춰 있게 된다는 것이다. 이것은
// **자가 치유 가능**하다: 리스가 만료되면 next_attempt_at이 과거가 되어, 다음 수령
// 라운드가 같은 행을 다시 집어 간다(수령 조건의 state IN ('pending','sending') 참고).
// 재시도 횟수는 수령 시점에 이미 +1 되므로, 크래시가 무한 재시도를 만들지는 않는다 —
// MaxNotifyAttempts번의 기회를 다 쓰면 failed로 떨어져 사람의 처리를 기다린다.

// MaxNotifyAttempts는 전달 한 건의 최대 시도 횟수다(첫 시도 포함).
// 전달 엔진이 아니라 여기서 정의하는 이유: 이것은 상태 머신 자체의 정책이고, 엔진은
// 그저 실행자일 뿐이다.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize는 하나의 다이제스트 배치가 한 번에 합칠 수 있는 최대 전달 건수다.
//
// 존재하는 이유는 리소스다: 하나의 다이제스트 주기 안에서 수만 개의 취약점이 쓸려
// 나오면(완전히 가능하다 — 전체 스캔 한 번으로도 그렇게 된다), 상한이 없으면 수령이
// 모든 행을 메모리에 읽어들여 하나의 초장문 메시지로 렌더링하고, 그 대부분이 채널의
// 길이 상한에 잘려나간다 — 메모리도 낭비하고, 잘려나간 취약점들을 **조용히 잃어버리게**
// 된다. 상한을 두면, 넘친 부분은 DB에 남아 다음 배치가 되고, 다음 주기에 자연스럽게
// 나가므로 유실되지 않는다.
//
// 500을 택한 근거: 메시지로 렌더링했을 때 WeCom의 4096바이트 상한 안에서도 "읽을 내용이
// 남아 있는" 수준이다; 더 크게 잡아도 잘림이 일어나는 위치가 더 뒤로 밀릴 뿐이다.
const MaxDigestBatchSize = 500

// NotificationDelivery는 전달 작업 한 건으로, 렌더링에 필요한 채널 설정과 이벤트 스냅샷을 담는다.
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// 조인으로 불러온 렌더링 컨텍스트이며, JSON에는 포함되지 않는다(DTO 조립은 server 계층에서 함).
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind는 이벤트에서 함께 가져오며, 이력 목록에서 바로 취약점 상세로 이동할 때 쓴다.
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind는 목록 표시용 중복 필드로, 프런트엔드의 추가 조회를 덜어준다.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery는 전달 행을 읽는 통일된 형태다: 전달 + 이벤트 스냅샷 + 채널 설정.
// 메시지 하나를 렌더링하려면 셋 다 필요하며, 따로 조회하면 세 번 왕복하게 된다.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery는 한 번의 수령을 기술한다: 먼저 sel로 후보를 골라 잠그고, 그것들을
// sending으로 바꾸며 리스를 연장한다. sel 안의 lease 위치는 호출자가 $n으로 자리를
// 잡고 직접 값을 전달한다.
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries는 어떤 채널의 기한이 된 실시간 전달을 최대 limit건까지 수령한다.
//
// 의도적으로 **채널 단위**로 수령하며 "전역으로 한 묶음 수령한 뒤 골라서 보낸다"는
// 방식을 쓰지 않는다: 속도 제한 게이트는 전달 엔진 안에서 채널별로 관리되므로, 이번
// 라운드에 이 채널이 몇 건을 더 보낼 수 있는지 먼저 알고 그만큼만 행을 수령해야
// 속도 제한이 재시도 횟수를 갉아먹지 않는다. 반대로 먼저 수령하고 나중에 버리면,
// 속도 제한에 막힌 행은 이미 attempts가 한 번 세어진 상태라, 3번의 예산이 순수한
// 대기만으로 소진되어 결국 failed로 떨어진다.
//
// 조건에는 "리스가 만료된 sending"도 포함된다 — 그것이 크래시 자가 치유가 작동하는
// 지점이다. lease는 전달 한 번의 최악 소요 시간(채널 HTTP 클라이언트 타임아웃 15초)보다
// 눈에 띄게 커야 한다. 그러지 않으면 같은 행이 두 dispatcher에 동시에 전달될 수 있다.
// 동시에 비활성화된 채널도 걸러낸다: 비활성화 작업이 기존 전달을 이미 skipped로
// 표시했지만, 여기서 한 번 더 막아 비활성화와 수령이 동시에 일어날 때의 누락을 막는다.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue는 해당 채널이 기한이 된 배치를 충분히 모았는지 보고한다: 발송 대기
// 전달이 존재하고, **가장 오래된 것**의 나이가 다이제스트 주기에 도달했는지를 본다.
//
// 판정 기준은 가장 오래된 전달의 나이이며 벽시계 시각이 아니다: 이렇게 하면 막 만든
// 채널이 정시에 맞춰져서 바로 한 건짜리 "다이제스트"를 뱉어내지 않고, 오래 쌓인
// 배치도 한 주기를 더 헛되이 기다리지 않는다.
//
// ClaimDigestBatch와 분리한 이유는 의미가 다르기 때문이다: 이 함수는 "보내야 하는가"만
// 답하고, 수령은 그 채널의 **전체** 발송 대기 행(아직 나이가 안 찬 것 포함)을 가져가야
// 한다 — 그러지 않으면 한 주기가 여러 메시지로 쪼개져서 다이제스트의 의미가 없어진다.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch는 어떤 채널의 현재 기한이 된 발송 대기 전달을 하나의 다이제스트
// 배치로 수령하며, 한 배치는 최대 MaxDigestBatchSize건이다.
//
// 같은 배치의 모든 전달은 batch_id를 공유하며, 집합 안의 최소 id를 배치 번호로 쓴다
// (안정적이고 읽기 쉬우며 별도 시퀀스가 필요 없다). 재시도 시에는 COALESCE로 원래
// 배치 번호를 보존하여, "이 N건은 함께 보낸 것"이라는 사실이 여러 번 재시도된 뒤에도 유지된다.
//
// id 오름차순으로 앞의 N건을 가져오며 무작위로 가져오지 않는다: 가장 먼저 생성된
// 전달이 가장 먼저 나가므로, 적체가 생겨도 "새 취약점이 먼저 나가고 오래된 취약점은
// 영원히 뒤로 밀리는" 기아 현상이 생기지 않는다.
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit은 **메모리 상한**으로, 호출자는 MaxDigestBatchSize를 전달한다; 여기서
	// 한 번 더 조여서 호출자가 더 큰 값을 넘기는 것을 막는다.
	//
	// 의도적으로 "속도 제한 할당량"을 배치 크기로 받지 않는다: 속도 제한의 단위는
	// 메시지 건수다 — 배치 하나는 메시지 하나만 보내고 토큰 하나만 소비하며, 이는
	// server 계층의 takeTokens가 차감한다 — 이것은 "한 배치에 취약점 몇 개를 담는가"와는
	// 전혀 다른 차원이다. 예전에 rate_per_min을 digest에도 적용하려고 라운드별 요청
	// 예산을 배치 크기로 넘겼다가, rate=20/min인 채널의 배치마다 취약점이 1개만
	// 담기게 되어, digest가 요약 문구만 붙은 실시간 푸시로 퇴화해버렸다. 속도 제한을
	// 바꾸려면 takeTokens의 want를 고치고, 여기는 건드리지 말 것.
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err := d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries는 "선택 + sending으로 전환하며 리스 연장 + 전체 행 읽기"를 하나의
// 트랜잭션 안에서 전부 수행한다. postClaim은 선택적 추가 단계다(다이제스트 배치가
// 이를 이용해 batch_id를 기록한다).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // 커밋이 성공하면 no-op

	ids, err := selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// sending으로 바꾸고 next_attempt_at을 미래로 미룬다: 이 미래 시각이 곧 리스
	// 만료 시각이므로, "리스가 아직 만료되지 않음"과 "아직 재시도 시각이 안 됨"이
	// 같은 조건식을 공유하게 되어 새 컬럼이 필요 없다.
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if postClaim != nil {
		if err := postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent는 전달 묶음을 전달 완료로 표시한다.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args := placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries는 전달 묶음을 pending으로 되돌리고 재시도 시각을 미룬다.
//
// 새로운 중간 상태를 도입하지 않고 pending으로 되돌리는 이유는 "몇 번의 기회가
// 남았는가"를 한 곳에서만(MaxNotifyAttempts) 표현하여, 재시도 정책이 바뀔 때마다
// 상태 머신의 분기가 늘어나지 않게 하기 위함이다.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args := placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries는 전달 묶음을 pending으로 되돌려 즉시 다시 수령 가능하게 하고,
// **수령 시 계산된 그 한 번의 시도를 취소**한다.
//
// 용도는 하나뿐이다: 다이제스트 메시지를 채널 길이 상한에 맞춰 여러 조각으로 나눠
// 보낼 때, 이번 조각에 담기지 못한 항목은 다음 배치로 남겨야 한다. 그것은 실패가
// 아니므로 재시도 예산을 소비해서는 안 된다 — 수령 시점에 attempts가 이미 낙관적으로
// +1 되어 있으므로, 여기서 반드시 다시 빼줘야 한다. 그러지 않으면 500건의 적체가
// 조각당 20건씩 25조각으로 나뉠 때, 뒤쪽 항목이 3번째 조각에서 MaxNotifyAttempts에
// 의해 failed로 판정되는데, 그 항목들은 한 번도 오류를 낸 적이 없다.
//
// GREATEST(...,0)은 "누군가 수동으로 재발송해서 attempts를 0으로 만든 뒤 여기로 또
// 들어오는" 상황을 받아내어, 카운트가 음수가 되지 않게 한다.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries는 전달 묶음을 최종 실패로 표시하여, 사람이 전달 이력에서 재발송하기를 기다린다.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// 플레이스홀더는 $3부터 시작한다: $1은 state, $2는 last_error.
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery는 전달 한 건을 수동으로 재발송한다: pending으로
// 재설정하고, 재시도 횟수를 0으로 초기화하며, 즉시 만료시킨다. 횟수를 초기화하는
// 것은 의도적이다 — 사람이 "재발송"을 누른다는 것은 이전 실패 원인들이 이미
// 처리됐다는 뜻이므로, 예전 횟수로 다시 제한하는 것은 말이 안 된다.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("전달 %d가 존재하지 않거나 현재 상태에서는 재발송할 수 없습니다", id)
	}
	return nil
}

// NotificationDeliveryFilter는 전달 이력의 조회 조건이다.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries는 전달 이력을 페이지네이션하여 반환하며, 최신 것이 앞에 온다.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args := f.where()

	var total int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError는 오류 메시지를 컬럼이 허용하는 길이로 자른다. 채널이 반환하는
// 응답 본문은 길어질 수 있어서(범용 Webhook이 자체 구축 서비스로 갈 때 특히 그렇다),
// 자르지 않으면 이력 목록의 페이로드가 비대해진다.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// 문자 경계로 되돌려서, 절반짜리 UTF-8 문자가 남아 프런트엔드에서 깨져 보이지 않게 한다.
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders는 start부터 시작하는 $n 플레이스홀더 문자열과 그에 대응하는 인자를
// 생성하여 IN (...) 에 사용한다. 예: start=3, ids=[7,8] → "$3,$4", [7,8].
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
