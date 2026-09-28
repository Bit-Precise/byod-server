package byodserver

import (
	"net/url"
	"testing"
)

func TestParseUserPagination(t *testing.T) {
	tests := []struct {
		name                   string
		query                  string
		page, pageSize, offset int
	}{
		{name: "defaults", query: "", page: 1, pageSize: 50, offset: 0},
		{name: "page based", query: "page=3&page_size=25", page: 3, pageSize: 25, offset: 50},
		{name: "legacy limit offset", query: "limit=20&offset=41", page: 3, pageSize: 20, offset: 41},
		{name: "invalid values use defaults", query: "page=0&page_size=101", page: 1, pageSize: 50, offset: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseUserPagination(mustParseQuery(t, test.query))
			if got.Page != test.page || got.PageSize != test.pageSize || got.Offset != test.offset {
				t.Fatalf("parseUserPagination(%q) = %+v, want page=%d page_size=%d offset=%d", test.query, got, test.page, test.pageSize, test.offset)
			}
		})
	}
}

func mustParseQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return values
}
