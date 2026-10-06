package notify

// Snapshot 는 notification_events.snapshot JSONB 컬럼의 계약이다. 쓰는 쪽은 db 계층의
// 취약점 저장 트랜잭션이고, 읽는 쪽은 server 계층의 전달 엔진과 필터 매칭이다. 정의를
// 이 패키지에 둔 이유는 이것이 「알림 도메인」의 페이로드이기 때문이다: db는 직렬화만
// 담당하고 필드 의미는 알지 못한다.
//
// 왜 취약점 필드를 중복 저장하고 렌더링 시점에 다시 조회하지 않는가: 취약점은 이후
// 이름·등급·상태가 바뀔 수 있는데, 푸시 내용은 **발생 당시**의 결론을 반영해야 한다——
// 다시 조회하면 「나중에 low로 바뀐」 위험한 오해를 줄 수 있다. 또한 이렇게 하면
// fan-out과 렌더링이 findings/tasks/assets 세 테이블을 JOIN할 필요가 없다.
type Snapshot struct {
	// 이벤트 유형: finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// kind=finding_status_changed 일 때만 값이 있다.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item 은 전송 대기 중인 취약점 한 건으로, 채널 렌더링에 쓰인다.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets 는 해석된 자산 표시명(도메인/IP 등)이다. server 계층에서 채워준다——
	// 본 패키지는 데이터베이스를 다루지 않으므로 이름을 얻을 수 없다.
	Assets []string
	// DetailURL 은 취약점 상세 링크이며, 비어 있으면 public_base_url이 설정되지
	// 않은 것으로 간주해 렌더링 시 생략한다.
	DetailURL string
	// 상태 변경 이벤트 전용; 두 값이 모두 있으면 「대기중 → 수정됨」처럼 렌더링한다.
	FromStatus string
	ToStatus   string
}

// IsStatusChange 는 이 항목이 상태 변경 이벤트인지 보고한다.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title 은 항목의 표시 제목을 반환한다: 사용자가 지정한 name을 우선 쓰고,
// 없으면 취약점 유형 vulnclass로 대체하며, 둘 다 없으면 플레이스홀더를
// 쓴다——빈 제목을 출력하는 일은 절대 없다.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(이름 없는 취약점)"
}

// Message 는 한 번의 채널 전송에 담기는 전체 내용이다.
type Message struct {
	// 단건 전송 시 길이는 1이고, 요약 전송(digest) 시에는 한 묶음 전체가 담긴다.
	// 빈 슬라이스는 허용되지 않으며, 호출자는 최소 한 건을 보장해야 한다.
	Items []Item
	// Batch=true 면 요약 메시지로 렌더링한다(제목을 바꾸고 시간 범위와 건수를
	// 함께 표시).
	Batch bool
	// WindowMinutes 는 요약 주기(분)이며, Batch=true일 때만 「최근 N분」 문구에
	// 쓰인다. 렌더링 시 time.Since로 계산하지 않고 설정에서 명시적으로 전달받도록
	// 한 것은, 렌더링을 결정적으로 유지해 테스트하기 쉽게 하기 위해서다.
	WindowMinutes int
	// HomeURL 은 플랫폼 대시보드 주소(전역 public_base_url)이며, 비어 있으면
	// 대시보드 링크를 붙이지 않는다.
	HomeURL string
}
