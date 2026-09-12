package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchBridgeImageDownloadsObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-image-bytes"))
	}))
	defer server.Close()
	data, err := fetchBridgeImage(server.URL + "/media/oss/a.jpg")
	if err != nil || string(data) != "fake-image-bytes" {
		t.Fatalf("fetch = %q, %v", data, err)
	}
}

func TestFetchBridgeImageRejectsBadTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	if _, err := fetchBridgeImage(server.URL + "/missing.png"); err == nil {
		t.Fatal("404 must return an error")
	}
	if _, err := fetchBridgeImage("ftp://example.com/a.png"); err == nil {
		t.Fatal("non-http scheme must return an error")
	}
}
