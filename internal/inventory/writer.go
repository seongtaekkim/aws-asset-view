package inventory

import (
	"encoding/csv"
	"io"
)

func WriteCSV(w io.Writer, records []AssetRecord) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader()); err != nil {
		return err
	}
	for _, r := range records {
		if err := cw.Write(r.csvRow()); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
