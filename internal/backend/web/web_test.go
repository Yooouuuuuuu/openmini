package web

import (
	"reflect"
	"testing"
)

func TestParseUsagePanelPage(t *testing.T) {
	// the 用量 page's two list items, as the panel's inner text joins them
	txt := "目前用量 info 已使用 6% 重設時間：下午6:40 每週上限 重設時間：11月5日下午12:40 已使用 1%"
	got := parseUsagePanel(txt)
	want := map[string]string{
		"current_used":   "6%",
		"current_resets": "下午6:40",
		"weekly_used":    "1%",
		"weekly_resets":  "11月5日下午12:40",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %q", k, got[k], v)
		}
	}
}

func TestModelsWithLevels(t *testing.T) {
	w := &Web{modelOptions: []string{"3.8 Flash", "3.1 Pro"}, levels: []string{"低", "中等", "高"}}
	want := []string{"3.8 Flash", "3.1 Pro", "3.8 Flash 低", "3.8 Flash 中等", "3.8 Flash 高", "3.1 Pro 低", "3.1 Pro 中等", "3.1 Pro 高"}
	if got := w.Models(); !reflect.DeepEqual(got, want) {
		t.Errorf("Models() = %v, want %v", got, want)
	}
	// a switch on top of a model gets no level variants
	w = &Web{modelOptions: []string{"3.1 Pro", "延伸思考"}, modeOptions: []string{"延伸思考"}, levels: []string{"高"}}
	if got := w.Models(); !reflect.DeepEqual(got, []string{"3.1 Pro", "延伸思考", "3.1 Pro 高"}) {
		t.Errorf("Models() with a switch = %v", got)
	}
}

func TestSplitLevel(t *testing.T) {
	w := &Web{levels: []string{"低", "中等", "高"}}
	cases := []struct{ in, model, level string }{
		{"3.1 Pro 高", "3.1 Pro", "高"},
		{"3.8 Flash 中等", "3.8 Flash", "中等"},
		{"3.1 Pro", "3.1 Pro", ""}, // plain: level left as it is
		{"高", "高", ""},             // a bare level is not a model plus level
	}
	for _, c := range cases {
		if m, l := w.splitLevel(c.in); m != c.model || l != c.level {
			t.Errorf("splitLevel(%q) = %q, %q; want %q, %q", c.in, m, l, c.model, c.level)
		}
	}
}
