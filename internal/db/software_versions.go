package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"anchor/internal/domain"
)

type softwareVersionsValue domain.SoftwareVersions

func (v softwareVersionsValue) Value() (driver.Value, error) {
	if v == nil {
		return "{}", nil
	}

	data, err := json.Marshal(domain.SoftwareVersions(v))
	if err != nil {
		return nil, err
	}

	return string(data), nil
}

func (v *softwareVersionsValue) Scan(src any) error {
	if src == nil {
		*v = softwareVersionsValue{}
		return nil
	}

	var data []byte
	switch value := src.(type) {
	case []byte:
		data = value
	case string:
		data = []byte(value)
	default:
		return fmt.Errorf("scan software versions: unsupported type %T", src)
	}

	if len(data) == 0 {
		*v = softwareVersionsValue{}
		return nil
	}

	var versions domain.SoftwareVersions
	if err := json.Unmarshal(data, &versions); err != nil {
		return err
	}

	*v = softwareVersionsValue(versions)
	return nil
}
