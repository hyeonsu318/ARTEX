package db

import "testing"

func rule(kind, pattern string, enabled bool) AssetInterceptRule {
	return AssetInterceptRule{Kind: kind, Pattern: pattern, Enabled: enabled}
}

func TestMatchAssetInterceptRules(t *testing.T) {
	cases := []struct {
		name    string
		rules   []AssetInterceptRule
		domains []string
		ips     []string
		urls    []string
		want    bool
		wantVal string
	}{
		{"내장 퍼지 정부 도메인 매칭", []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
			[]string{"www.beijing.gov.cn"}, nil, nil, true, "www.beijing.gov.cn"},
		{"퍼지 교육 도메인 매칭", []AssetInterceptRule{rule("fuzzy_domain", ".edu", true)},
			[]string{"mit.edu"}, nil, nil, true, "mit.edu"},
		{"완전 일치 도메인 매칭, 대소문자 구분 없음", []AssetInterceptRule{rule("exact_domain", "Example.com", true)},
			[]string{"example.com"}, nil, nil, true, "example.com"},
		{"완전 일치 도메인은 서브도메인에 매칭되지 않음", []AssetInterceptRule{rule("exact_domain", "example.com", true)},
			[]string{"a.example.com"}, nil, nil, false, ""},
		{"완전 일치 IP 매칭", []AssetInterceptRule{rule("exact_ip", "203.0.113.5", true)},
			nil, []string{"203.0.113.5"}, nil, true, "203.0.113.5"},
		{"퍼지 IP 접두사 매칭", []AssetInterceptRule{rule("fuzzy_ip", "203.0.113.", true)},
			nil, []string{"203.0.113.99"}, nil, true, "203.0.113.99"},
		{"CIDR 매칭", []AssetInterceptRule{rule("cidr", "192.168.0.0/16", true)},
			nil, []string{"192.168.5.20"}, nil, true, "192.168.5.20"},
		{"CIDR 매칭 안 됨", []AssetInterceptRule{rule("cidr", "192.168.0.0/16", true)},
			nil, []string{"10.0.0.1"}, nil, false, ""},
		{"완전 일치 URL 매칭", []AssetInterceptRule{rule("exact_url", "https://a.gov.cn/login", true)},
			nil, nil, []string{"https://a.gov.cn/login"}, true, "https://a.gov.cn/login"},
		{"퍼지 URL 경로 매칭", []AssetInterceptRule{rule("fuzzy_url", "/admin", true)},
			nil, nil, []string{"https://x.com/admin/panel"}, true, "https://x.com/admin/panel"},
		{"비활성화된 규칙은 매칭되지 않음", []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", false)},
			[]string{"www.gov.cn"}, nil, nil, false, ""},
		{"규칙 없음, 매칭 안 됨", nil, []string{"www.gov.cn"}, nil, nil, false, ""},
		{"빈 pattern은 매칭되지 않음", []AssetInterceptRule{rule("fuzzy_domain", "  ", true)},
			[]string{"www.gov.cn"}, nil, nil, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, val, ok := MatchAssetInterceptRules(c.rules, c.domains, c.ips, c.urls)
			if ok != c.want {
				t.Fatalf("매칭 결과 = %v, 기대값 %v (rule=%+v)", ok, c.want, r)
			}
			if ok && val != c.wantVal {
				t.Fatalf("매칭된 값 = %q, 기대값 %q", val, c.wantVal)
			}
		})
	}
}

func TestEvaluateAssetGate(t *testing.T) {
	block := []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)}
	allow := []AssetInterceptRule{rule("fuzzy_domain", "example.com", true)}

	// 1. 차단 규칙에 매칭됨 → 거부(차단 이유가 우선).
	if d := EvaluateAssetGate(block, allow, []string{"www.gov.cn"}, nil, nil); d.Allowed {
		t.Fatal("차단 규칙에 매칭되면 거부되어야 한다")
	}

	// 2. 차단에는 매칭되지 않지만, 허용 규칙이 있는데 매칭되지 않음 → 거부(허용되지 않음).
	d := EvaluateAssetGate(block, allow, []string{"foo.other.com"}, nil, nil)
	if d.Allowed {
		t.Fatal("화이트리스트가 있는데 매칭되지 않으면 거부되어야 한다")
	}
	if d.Reason == "" {
		t.Fatal("거부에는 이유가 있어야 한다")
	}

	// 3. 차단에는 매칭되지 않고, 허용 규칙에 매칭됨 → 통과.
	if d := EvaluateAssetGate(block, allow, []string{"api.example.com"}, nil, nil); !d.Allowed {
		t.Fatal("화이트리스트에 매칭되면 통과되어야 한다")
	}

	// 4. 허용 규칙이 없음(화이트리스트 비활성화) → 차단에 매칭되지 않으면 바로 통과.
	if d := EvaluateAssetGate(block, nil, []string{"foo.other.com"}, nil, nil); !d.Allowed {
		t.Fatal("화이트리스트가 없을 때는 차단에 매칭되지 않으면 통과되어야 한다")
	}

	// 5. 허용 규칙이 전부 비활성화됨 → 화이트리스트가 비활성화된 것으로 간주하여 통과.
	disabledAllow := []AssetInterceptRule{rule("fuzzy_domain", "example.com", false)}
	if d := EvaluateAssetGate(nil, disabledAllow, []string{"foo.other.com"}, nil, nil); !d.Allowed {
		t.Fatal("화이트리스트가 전부 비활성화됐을 때는 통과되어야 한다")
	}

	// 6. 차단이 허용보다 우선: 같은 대상이 차단과 허용 모두에 매칭됨 → 거부.
	if d := EvaluateAssetGate(
		[]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
		[]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
		[]string{"www.gov.cn"}, nil, nil,
	); d.Allowed {
		t.Fatal("차단이 허용보다 우선해야 한다")
	}
}

func TestAssetInterceptCandidates(t *testing.T) {
	// URL만 있는 서비스 자산: host가 분리되어 도메인 후보에 들어가야 하며, 그래서 fuzzy_domain에 매칭되어야 한다.
	a := &Asset{Type: "service", URL: "https://portal.beijing.gov.cn:8443/app"}
	domains, _, urls := a.interceptCandidates()
	if len(urls) != 1 || urls[0] != a.URL {
		t.Fatalf("urls = %v", urls)
	}
	found := false
	for _, d := range domains {
		if d == "portal.beijing.gov.cn" {
			found = true
		}
	}
	if !found {
		t.Fatalf("URL host가 도메인 후보로 분리되지 않음: %v", domains)
	}
	r, _, ok := MatchAssetInterceptRules([]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)}, domains, nil, urls)
	if !ok {
		t.Fatalf("URL만 있는 정부 서비스 자산은 fuzzy_domain에 매칭되어야 한다, rule=%+v", r)
	}

	// URL host가 IP일 때는 IP 후보로 분류되어, CIDR에 매칭될 수 있어야 한다.
	b := &Asset{Type: "service", URL: "http://10.1.2.3/x"}
	_, ips, _ := b.interceptCandidates()
	if r, _, ok := MatchAssetInterceptRules([]AssetInterceptRule{rule("cidr", "10.0.0.0/8", true)}, nil, ips, nil); !ok {
		t.Fatalf("URL 안의 IP는 CIDR에 매칭되어야 한다, ips=%v rule=%+v", ips, r)
	}
}
