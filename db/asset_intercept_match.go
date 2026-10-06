package db

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// 자산 차단 규칙의 매칭/실행 계층. asset_intercept.go는 규칙 저장만 담당하고,
// 여기서는 "대상 자산"의 도메인/IP/URL을 활성화된 규칙과 매칭한다. agent 도구
// (add_intent, insert_assets)가 의도를 내려보내거나 자산을 삽입하기 전에 호출하며,
// 매칭되면 거부한다.

// AssetInterceptKindLabel은 kind의 한국어 레이블을 반환한다. agent에게 보여줄 설명 메시지에 쓴다.
func AssetInterceptKindLabel(kind string) string {
	switch kind {
	case "exact_domain":
		return "도메인(완전 일치)"
	case "exact_ip":
		return "IP(완전 일치)"
	case "exact_url":
		return "URL(완전 일치)"
	case "fuzzy_domain":
		return "도메인(퍼지)"
	case "fuzzy_ip":
		return "IP(퍼지)"
	case "fuzzy_url":
		return "URL(퍼지)"
	case "cidr":
		return "CIDR 대역"
	}
	return kind
}

// Reason은 읽을 수 있는 매칭 이유를 반환한다. 예: 자산 차단 규칙에 매칭됨 [도메인(퍼지): .gov.cn](비고).
func (r AssetInterceptRule) Reason() string {
	s := fmt.Sprintf("자산 차단 규칙에 매칭됨 [%s: %s]", AssetInterceptKindLabel(r.Kind), r.Pattern)
	if note := strings.TrimSpace(r.Note); note != "" {
		s += "(" + note + ")"
	}
	return s
}

// matchOne은 활성화된 규칙 하나가 주어진 도메인/IP/URL 후보 문자열에 매칭되는지 판단하고, 매칭된 값을 반환한다.
func matchOne(r AssetInterceptRule, domains, ips, urls []string) (string, bool) {
	p := strings.TrimSpace(r.Pattern)
	if p == "" {
		return "", false
	}
	switch r.Kind {
	case "exact_domain":
		for _, d := range domains {
			if strings.EqualFold(strings.TrimSpace(d), p) {
				return d, true
			}
		}
	case "exact_ip":
		for _, ip := range ips {
			if strings.TrimSpace(ip) == p {
				return ip, true
			}
		}
	case "exact_url":
		for _, u := range urls {
			if strings.TrimSpace(u) == p {
				return u, true
			}
		}
	case "fuzzy_domain":
		lp := strings.ToLower(p)
		for _, d := range domains {
			if d != "" && strings.Contains(strings.ToLower(d), lp) {
				return d, true
			}
		}
	case "fuzzy_ip":
		for _, ip := range ips {
			if ip != "" && strings.Contains(ip, p) {
				return ip, true
			}
		}
	case "fuzzy_url":
		lp := strings.ToLower(p)
		for _, u := range urls {
			if u != "" && strings.Contains(strings.ToLower(u), lp) {
				return u, true
			}
		}
	case "cidr":
		_, ipnet, err := net.ParseCIDR(p)
		if err != nil {
			return "", false
		}
		for _, ip := range ips {
			if pip := net.ParseIP(strings.TrimSpace(ip)); pip != nil && ipnet.Contains(pip) {
				return ip, true
			}
		}
	}
	return "", false
}

// MatchAssetInterceptRules는 주어진 도메인/IP/URL 후보 문자열에 매칭되는 첫 번째 활성 규칙과
// 매칭된 값을 반환한다. insert_assets가 아직 저장되지 않은 원본 입력(assetInputItem)을
// 매칭할 때 사용한다.
func MatchAssetInterceptRules(rules []AssetInterceptRule, domains, ips, urls []string) (AssetInterceptRule, string, bool) {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if v, ok := matchOne(r, domains, ips, urls); ok {
			return r, v, true
		}
	}
	return AssetInterceptRule{}, "", false
}

// interceptCandidates는 이미 저장된 자산에서 차단 매칭용 도메인/IP/URL 후보 문자열을 추출한다.
// URL의 host는 분리되어 분류되므로, "URL만 있는" 서비스 자산도 도메인/IP 규칙에 매칭될 수 있다.
func (a *Asset) interceptCandidates() (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, a.Domain)
	add(&domains, a.RootDomain)
	for _, d := range a.BoundDomains {
		add(&domains, d)
	}
	add(&ips, a.IP)
	add(&urls, a.URL)
	if a.URL != "" {
		if u, err := url.Parse(a.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// InterceptLabel은 자산의 짧은 식별자를 반환한다. agent에게 보여줄 설명 메시지에 쓴다.
func (a *Asset) InterceptLabel() string {
	var target string
	switch {
	case a.Domain != "":
		target = a.Domain
	case a.URL != "":
		target = a.URL
	case a.IP != "":
		target = a.IP
	default:
		target = fmt.Sprintf("#%d", a.ID)
	}
	return fmt.Sprintf("자산#%d[%s] %s", a.ID, a.Type, target)
}

// hasEnabledRule은 규칙 집합에 활성화된 규칙이 하나라도 있는지 판단한다.
func hasEnabledRule(rules []AssetInterceptRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// AssetGateDecision은 "먼저 차단, 그 다음 허용" 게이트가 후보 문자열 집합에 내린 판정 결과다.
type AssetGateDecision struct {
	Allowed bool
	Reason  string // 거부 이유(자산 식별자는 포함하지 않음); Allowed=true면 비어 있음
}

// EvaluateAssetGate는 작업 단위 게이트 판정을 수행한다:
//  1. 활성화된 blockRules 중 하나라도 매칭되면 → 거부(차단 이유).
//  2. 그렇지 않고 allowRules에 활성 항목이 있는데 전부 매칭되지 않으면 → 거부(허용 범위 밖).
//  3. 그 외에는 통과.
//
// allowRules가 비어 있거나 활성 항목이 없으면 허용 게이트는 작동하지 않는다(화이트리스트를
// 켜지 않은 것으로 간주해 전부 통과시킨다). "허용 규칙을 설정하지 않았다"는 이유로 모든
// 자산이 막히는 것을 막기 위함이다.
func EvaluateAssetGate(blockRules, allowRules []AssetInterceptRule, domains, ips, urls []string) AssetGateDecision {
	if rule, _, ok := MatchAssetInterceptRules(blockRules, domains, ips, urls); ok {
		return AssetGateDecision{Allowed: false, Reason: rule.Reason()}
	}
	if hasEnabledRule(allowRules) {
		if _, _, ok := MatchAssetInterceptRules(allowRules, domains, ips, urls); !ok {
			return AssetGateDecision{Allowed: false, Reason: "작업 허용(화이트리스트) 범위에 없어 테스트가 허용되지 않습니다"}
		}
	}
	return AssetGateDecision{Allowed: true}
}

// AssetInterceptHit는 게이트에서 거부된 자산 하나를 나타낸다(차단 매칭 또는 허용 범위 밖).
type AssetInterceptHit struct {
	Asset  *Asset
	Reason string // 읽을 수 있는 이유
}

// Describe는 읽을 수 있는 설명을 반환한다: 자산 정보 + 이유.
func (h AssetInterceptHit) Describe() string {
	return fmt.Sprintf("%s → %s", h.Asset.InterceptLabel(), h.Reason)
}

// ListAssetInterceptRules는 *DB의 동명 메서드를 그대로 전달하여, AssetStore만 가진
// 호출자(예: agent 도구)도 규칙을 읽을 수 있게 한다.
func (s *AssetStore) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	return s.db.ListAssetInterceptRules()
}

// CheckAssetsIntercept는 id로 자산을 불러와 각각에 "먼저 차단, 그 다음 허용" 게이트 판정을
// 수행하고, 거부된 모든 자산을 반환한다. 차단 규칙 = 전역 ∪ 작업 단위 block; 허용 규칙 =
// 작업 단위 allow(해당 작업에만 적용). id가 없으면 바로 반환한다. 차단이 scope에 의해
// 약화되지 않도록 전역 GetByIDs(작업 범위 필터를 받지 않음)를 사용한다.
func (s *AssetStore) CheckAssetsIntercept(taskID int64, ids []int64) ([]AssetInterceptHit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	blockRules, err := s.db.ListAssetInterceptRules()
	if err != nil {
		return nil, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err := s.TaskInterceptRulesSplit(taskID)
		if err != nil {
			return nil, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	// 차단 규칙도 없고 활성화된 허용 규칙도 없으면 → 판정 불필요, 전부 통과.
	if len(blockRules) == 0 && !hasEnabledRule(allowRules) {
		return nil, nil
	}
	assets, err := s.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	var hits []AssetInterceptHit
	for _, a := range assets {
		domains, ips, urls := a.interceptCandidates()
		if d := EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
			hits = append(hits, AssetInterceptHit{Asset: a, Reason: d.Reason})
		}
	}
	return hits, nil
}
