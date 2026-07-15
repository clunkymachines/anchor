package domain

import (
	"errors"
	"net/url"
)

func ValidateCoAPFrontendURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("CoAP frontend URL must be an absolute HTTP URL without credentials, query, or fragment")
	}
	return nil
}
