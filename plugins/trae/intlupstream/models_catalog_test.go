package upstream

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestModelsCatalogSupportedShapes(t *testing.T) {
	for _, body := range []string{`{"code":0,"data":[{"name":"a"},{"name":"b"}]}`, `{"code":0,"data":{"list":[{"function":"solo_agent","models":[{"name":"a","context_window_tokens":{"dev":200000,"max":1000000}},{"name":"b"},{"name":"a"},{"name":""}]}]}}`, `{"code":0,"data":{"list":[{"function":"one","models":[{"name":"a"}]},{"function":"two","models":[{"name":"b"},{"name":"a"}]}]}}`} {
		got, err := parseModelsCatalog([]byte(body))
		if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("got %v err %v", got, err)
		}
	}
}
func TestModelsCatalogRejectsBusinessErrorsBeforeShape(t *testing.T) {
	for _, body := range []string{`{"code":1001,"data":{}}`, `{"code":1001,"data":{"list":[{"models":[{"name":"fake"}]}]}}`} {
		_, err := parseModelsCatalog([]byte(body))
		if err == nil || !strings.Contains(err.Error(), "1001") {
			t.Fatalf("business error lost %v", err)
		}
	}
	for _, body := range []string{`{}`, `{"code":0,"data":null}`, `{"data":[]}`, `{"data":{}}`, `{"data":{"list":[]}}`, `{"data":{"models":[{"name":"unexpected"}]}}`, `{"data":{"list":[{"models":"unexpected"}]}}`, `{"data":[{"name":7}]}`, `{"data":"unexpected"}`, `{"data":[{"name":"a"}]}garbage`} {
		if got, err := parseModelsCatalog([]byte(body)); err == nil {
			t.Fatalf("accepted %s => %v", body, got)
		}
	}
}
func TestFetchModelsGroupedWireAndBound(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Method != "GET" || r.URL.Path != "/models" || r.Header.Get("Authorization") != "Cloud-IDE-JWT mock" {
			t.Error("request contract changed")
		}
		if count == 1 {
			_, _ = w.Write([]byte(`{"code":0,"data":{"list":[{"function":"solo_agent","models":[{"name":"wire"}]}]}}`))
		} else {
			_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	a := &Auth{AccessToken: "mock"}
	got, err := c.FetchModels(a)
	if err != nil || len(got) != 1 || got[0] != "wire" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err = c.FetchModels(a); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversize %v", err)
	}
	if count != 2 {
		t.Fatal("retried")
	}
}
