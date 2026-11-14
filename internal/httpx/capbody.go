package httpx

import (
	"io"
	"net/http"
)

type capTransport struct {
	base http.RoundTripper
	max  int64
}

func (c *capTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &cappedBody{rc: resp.Body, remaining: c.max}
	return resp, nil
}

type cappedBody struct {
	rc        io.ReadCloser
	remaining int64
	end       error
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.end != nil {
		return 0, c.end
	}
	if c.remaining <= 0 {
		var one [1]byte
		if n, _ := io.ReadAtLeast(c.rc, one[:], 1); n > 0 {
			c.end = ErrResponseTruncated
		} else {
			c.end = io.EOF
		}
		return 0, c.end
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.rc.Read(p)
	c.remaining -= int64(n)
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }
