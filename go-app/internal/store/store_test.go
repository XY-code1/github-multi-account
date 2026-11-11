package store

import "testing"

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSaveThrottle(t *testing.T) {
	s := newTestStore(t)

	ok, err := s.SaveSnapshot("u1", Metrics{Followers: 1}, true)
	if err != nil || !ok {
		t.Fatalf("首次快照应保存成功: ok=%v err=%v", ok, err)
	}
	ok, _ = s.SaveSnapshot("u1", Metrics{Followers: 2}, false)
	if ok {
		t.Fatal("10 分钟内的重复快照应被节流")
	}
	ok, _ = s.SaveSnapshot("u1", Metrics{Followers: 3}, true)
	if !ok {
		t.Fatal("force=true 应绕过节流")
	}

	h, err := s.History("u1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 {
		t.Fatalf("期望 2 条历史，得到 %d", len(h))
	}
	if h[0].Followers != 1 || h[1].Followers != 3 {
		t.Fatalf("历史顺序错误: %d, %d", h[0].Followers, h[1].Followers)
	}
}

func TestHistoryPerUserIsolated(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.SaveSnapshot("u1", Metrics{Followers: 1}, true)
	_, _ = s.SaveSnapshot("u2", Metrics{Followers: 9}, true)

	h1, _ := s.History("u1", 100)
	h2, _ := s.History("u2", 100)
	if len(h1) != 1 || len(h2) != 1 {
		t.Fatalf("历史应彼此隔离: u1=%d u2=%d", len(h1), len(h2))
	}
	if h1[0].Followers != 1 || h2[0].Followers != 9 {
		t.Fatal("历史内容错误")
	}
}

func TestDeleteHistory(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.SaveSnapshot("u1", Metrics{}, true)
	_, _ = s.SaveSnapshot("u1", Metrics{}, true)
	_, _ = s.SaveSnapshot("u2", Metrics{}, true)

	n, err := s.DeleteHistory("u1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("期望删除 2 条，得到 %d", n)
	}
	if h, _ := s.History("u1", 100); len(h) != 0 {
		t.Fatal("u1 历史应清空")
	}
	if h, _ := s.History("u2", 100); len(h) != 1 {
		t.Fatal("u2 历史不应受影响")
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 5; i++ {
		_, _ = s.SaveSnapshot("u1", Metrics{Followers: i}, true)
	}
	n, err := s.Prune(2, 90)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("期望删除 3 条，得到 %d", n)
	}
	h, _ := s.History("u1", 100)
	if len(h) != 2 {
		t.Fatalf("期望保留 2 条，得到 %d", len(h))
	}
	if h[0].Followers != 3 || h[1].Followers != 4 {
		t.Fatalf("应保留最新的两条: %d, %d", h[0].Followers, h[1].Followers)
	}
}

func TestHistoryLimitClamped(t *testing.T) {
	s := newTestStore(t)
	got, err := s.History("nobody", -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("不存在的账号应返回空数组")
	}
}
