package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// releaseCache 는 GitHub 할당량을 보호하는 계층이다: 인증되지 않은 API 는 IP 당 시간당 60회뿐인데,
// 상단 바의 "새 버전 있음" 알림은 페이지 전체를 로드할 때마다 한 번 조회한다. 캐시가 무효화되면 사용자가 탭을 여러 개 열기만 해도
// 할당량이 바닥나고, 나중에 정말 업데이트하려 할 때 오히려 조회할 수 없게 된다.

func newTestCache(fetch func(context.Context, *http.Client) (*selfupdate.Release, error)) *releaseCache {
	return &releaseCache{fetch: fetch}
}

func TestReleaseCacheServesFromCache(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	for range 5 {
		rel, err := c.get(t.Context(), nil, false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rel.TagName != "v0.3.8" {
			t.Fatalf("TagName = %q", rel.TagName)
		}
	}
	if calls != 1 {
		t.Errorf("5회 조회 중 원본 조회는 1회여야 하나 실제로는 %d회", calls)
	}
}

func TestReleaseCacheForceBypasses(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// 사용자가 「업데이트 확인」을 누르면 실시간 결과를 받아야 한다. 그렇지 않으면 방금 배포된 버전이 캐시 만료 후에야 보인다.
	if _, err := c.get(t.Context(), nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("force 는 캐시를 우회해야 하며 원본 조회 2회가 기대되나 실제로는 %d회", calls)
	}
}

func TestReleaseCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// 저장 시각을 방금 만료되도록 앞으로 당겨 TTL 도래를 시뮬레이션한다.
	c.at = time.Now().Add(-releaseTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("TTL 만료 후에는 원본을 다시 조회해야 하며 2회가 기대되나 실제로는 %d회", calls)
	}
}

func TestReleaseCacheUsesShorterTTLForErrors(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return nil, errors.New("github 에 도달할 수 없음")
	})

	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("오류가 반환되어야 함")
	}
	// 실패 결과도 잠시 캐시해야 한다. 그렇지 않으면 GitHub 에 도달할 수 없을 때 페이지를 로드할 때마다 타임아웃을 헛되이 기다린다.
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("오류가 반환되어야 함")
	}
	if calls != 1 {
		t.Errorf("오류는 짧게 캐시되어야 하며 원본 조회 1회가 기대되나 실제로는 %d회", calls)
	}

	// 다만 오류의 TTL 은 성공의 TTL 보다 확실히 짧아야 하며, 네트워크가 복구되면 빠르게 자가 회복되어야 한다.
	if releaseErrTTL >= releaseTTL {
		t.Fatalf("오류 TTL(%v) 은 성공 TTL(%v) 보다 짧아야 함", releaseErrTTL, releaseTTL)
	}
	c.at = time.Now().Add(-releaseErrTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("오류가 반환되어야 함")
	}
	if calls != 2 {
		t.Errorf("오류 TTL 만료 후에는 재시도해야 하며 2회가 기대되나 실제로는 %d회", calls)
	}
}

func TestReleaseCacheDoesNotPoisonOnCallerCancel(t *testing.T) {
	good := &selfupdate.Release{TagName: "v0.3.8"}
	c := newTestCache(func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return good, nil
	})
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}

	// 방문자가 탭을 닫으면 요청이 취소된다. 그것이 GitHub 의 문제를 뜻하지는 않으므로 "취소됨"을
	// 캐시에 기록해서는 안 된다. 그렇지 않으면 이후 30분 동안 모든 방문자가 영문 모를 오류를 받게 된다.
	c.fetch = func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return nil, ctx.Err()
	}
	c.at = time.Now().Add(-releaseTTL - time.Second) // 캐시를 만료시켜 원본을 다시 조회하게 한다

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, nil, false); err == nil {
		t.Fatal("호출자가 취소했을 때는 오류를 그대로 전달해야 함")
	}

	// 핵심 불변식: 취소된 그 한 번은 어떤 흔적도 남기지 않는다 — 캐시에 "취소됨" 오류도 없고,
	// 직전의 정상 결과도 그대로 유지된다.
	if c.err != nil {
		t.Fatalf("취소 오류는 캐시에 기록되면 안 되며, 실제 값 %v", c.err)
	}
	if c.rel == nil || c.rel.TagName != "v0.3.8" {
		t.Fatalf("캐시는 직전의 정상 결과를 유지해야 하며, 실제 값 %+v", c.rel)
	}

	// 그 취소로는 새 데이터를 얻지 못했으므로 다음 방문자는 원본을 다시 조회해야 하며 — 정상적으로 결과를 받을 수 있고,
	// 직전의 취소에 영향을 받지 않는다.
	c.fetch = func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return good, nil
	}
	rel, err := c.get(t.Context(), nil, false)
	if err != nil {
		t.Fatalf("취소 이후의 정상 요청은 오류가 없어야 함: %v", err)
	}
	if rel == nil || rel.TagName != "v0.3.8" {
		t.Fatalf("정상 결과를 받아야 하며, 실제 값 %+v", rel)
	}
}
