package postman

import "encoding/json"

type pmText string

func (t *pmText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = pmText(s)
		return nil
	}
	var o struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(b, &o) == nil {
		*t = pmText(o.Content)
	}
	return nil
}

type pmKV struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description pmText `json:"description"`
	Disabled    bool   `json:"disabled"`
	Type        string `json:"type"`
}

type pmURL struct {
	Raw      string `json:"raw"`
	Query    []pmKV `json:"query"`
	Variable []pmKV `json:"variable"`
}

func (u *pmURL) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		u.Raw = s
		return nil
	}
	type plain pmURL
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*u = pmURL(p)
	return nil
}

type pmBody struct {
	Mode       string `json:"mode"`
	Raw        string `json:"raw"`
	URLEncoded []pmKV `json:"urlencoded"`
	FormData   []pmKV `json:"formdata"`
	Options    struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
}

type pmRequest struct {
	Method      string          `json:"method"`
	URL         pmURL           `json:"url"`
	Header      []pmKV          `json:"header"`
	Body        *pmBody         `json:"body"`
	Description pmText          `json:"description"`
	Auth        json.RawMessage `json:"auth"`
}

type pmItem struct {
	Name        string     `json:"name"`
	Description pmText     `json:"description"`
	Item        []pmItem   `json:"item"`
	Request     *pmRequest `json:"request"`
}

type pmCollection struct {
	Info struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	} `json:"info"`
	Item     []pmItem        `json:"item"`
	Variable []pmKV          `json:"variable"`
	Auth     json.RawMessage `json:"auth"`
}
