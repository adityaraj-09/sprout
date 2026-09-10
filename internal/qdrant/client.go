package qdrant

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Client struct {
	BaseURL  string
	APIKey   string
	Insecure bool
	HTTP     *http.Client
}

func NewClient(c Conn) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(c.BaseURL(), "/"),
		APIKey:   c.APIKey,
		Insecure: c.Insecure || (c.TLS && (c.Host == "127.0.0.1" || c.Host == "localhost")),
	}
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.Insecure {
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{}
		}
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
		tr.TLSClientConfig.InsecureSkipVerify = true
	}
	c.HTTP = &http.Client{Transport: tr, Timeout: 10 * time.Minute}
	return c.HTTP
}

func (c *Client) Ready(ctx context.Context) error {
	if err := c.do(ctx, http.MethodGet, "/readyz", nil, nil); err == nil {
		return nil
	}
	return c.do(ctx, http.MethodGet, "/", nil, nil)
}

type collectionRef struct {
	Name string `json:"name"`
}

func (c *Client) ListCollections(ctx context.Context) ([]string, error) {
	var out struct {
		Result struct {
			Collections []collectionRef `json:"collections"`
		} `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, "/collections", nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Result.Collections))
	for _, col := range out.Result.Collections {
		if col.Name != "" {
			names = append(names, col.Name)
		}
	}
	return names, nil
}

type CollectionInfo struct {
	Status      string `json:"status"`
	PointsCount int64  `json:"points_count"`
	Config      struct {
		Params struct {
			Vectors        json.RawMessage `json:"vectors"`
			SparseVectors  json.RawMessage `json:"sparse_vectors"`
			ShardNumber    int             `json:"shard_number"`
			OnDiskPayload  bool            `json:"on_disk_payload"`
			ReplicationFac int             `json:"replication_factor"`
		} `json:"params"`
	} `json:"config"`
}

func (c *Client) CollectionInfo(ctx context.Context, name string) (CollectionInfo, error) {
	var out struct {
		Result CollectionInfo `json:"result"`
	}
	if err := c.do(ctx, http.MethodGet, "/collections/"+url.PathEscape(name), nil, &out); err != nil {
		return CollectionInfo{}, err
	}
	return out.Result, nil
}

func (c *Client) CollectionCounts(ctx context.Context) (map[string]int64, error) {
	names, err := c.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, name := range names {
		info, err := c.CollectionInfo(ctx, name)
		if err != nil {
			return nil, err
		}
		out[name] = info.PointsCount
	}
	return out, nil
}

type snapshotInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (c *Client) CreateSnapshot(ctx context.Context, collection string) (snapshotInfo, error) {
	var out struct {
		Result snapshotInfo `json:"result"`
	}
	path := "/collections/" + url.PathEscape(collection) + "/snapshots?wait=true"
	if err := c.do(ctx, http.MethodPost, path, map[string]any{}, &out); err != nil {
		if err2 := c.do(ctx, http.MethodPut, path, map[string]any{}, &out); err2 != nil {
			return snapshotInfo{}, err
		}
	}
	return out.Result, nil
}

func (c *Client) DownloadSnapshot(ctx context.Context, collection, snapName, dest string) error {
	path := "/collections/" + url.PathEscape(collection) + "/snapshots/" + url.PathEscape(snapName)
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("qdrant %s %s: %s %s", http.MethodGet, path, res.Status, strings.TrimSpace(string(b)))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, res.Body)
	return err
}

func (c *Client) DeleteSnapshot(ctx context.Context, collection, snapName string) error {
	path := "/collections/" + url.PathEscape(collection) + "/snapshots/" + url.PathEscape(snapName)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) UploadSnapshot(ctx context.Context, collection, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var werr error
		defer func() {
			_ = mw.Close()
			_ = pw.CloseWithError(werr)
		}()
		fw, err := mw.CreateFormFile("snapshot", filepath.Base(filePath))
		if err != nil {
			werr = err
			return
		}
		_, werr = io.Copy(fw, f)
	}()
	path := "/collections/" + url.PathEscape(collection) + "/snapshots/upload?wait=true&priority=snapshot"
	req, err := c.newRequest(ctx, http.MethodPost, path, pr)
	if err != nil {
		_ = pr.Close()
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
	if res.StatusCode >= 300 {
		return fmt.Errorf("qdrant upload snapshot %s: %s %s", collection, res.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) CreateCollection(ctx context.Context, name string, info CollectionInfo) error {
	body := map[string]any{}
	if len(info.Config.Params.Vectors) > 0 && string(info.Config.Params.Vectors) != "null" {
		var vectors any
		if err := json.Unmarshal(info.Config.Params.Vectors, &vectors); err == nil {
			body["vectors"] = vectors
		}
	} else {
		body["vectors"] = map[string]any{}
	}
	if len(info.Config.Params.SparseVectors) > 0 && string(info.Config.Params.SparseVectors) != "null" {
		var sparse any
		if err := json.Unmarshal(info.Config.Params.SparseVectors, &sparse); err == nil {
			body["sparse_vectors"] = sparse
		}
	}
	if info.Config.Params.OnDiskPayload {
		body["on_disk_payload"] = true
	}
	return c.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(name), body, nil)
}

func (c *Client) ScrollCopy(ctx context.Context, dest *Client, collection string) error {
	info, err := c.CollectionInfo(ctx, collection)
	if err != nil {
		return err
	}
	if err := dest.CreateCollection(ctx, collection, info); err != nil {
		return fmt.Errorf("create collection %s: %w", collection, err)
	}
	var offset any
	for {
		req := map[string]any{
			"limit":        100,
			"with_payload": true,
			"with_vector":  true,
		}
		if offset != nil {
			req["offset"] = offset
		}
		var out struct {
			Result struct {
				Points         []json.RawMessage `json:"points"`
				NextPageOffset any               `json:"next_page_offset"`
			} `json:"result"`
		}
		if err := c.do(ctx, http.MethodPost, "/collections/"+url.PathEscape(collection)+"/points/scroll", req, &out); err != nil {
			return fmt.Errorf("scroll %s: %w", collection, err)
		}
		if len(out.Result.Points) > 0 {
			if err := dest.do(ctx, http.MethodPut, "/collections/"+url.PathEscape(collection)+"/points?wait=true",
				map[string]any{"points": out.Result.Points}, nil); err != nil {
				return fmt.Errorf("upsert %s: %w", collection, err)
			}
		}
		if out.Result.NextPageOffset == nil {
			break
		}
		offset = out.Result.NextPageOffset
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("qdrant %s %s: %s %s", method, path, res.Status, strings.TrimSpace(string(b)))
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("qdrant decode %s: %w (%s)", path, err, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("api-key", c.APIKey)
	}
	return req, nil
}
