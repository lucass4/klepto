package query

import (
	"fmt"
	"io"
	"strconv"
	"time"

	sq "github.com/Masterminds/squirrel"
	wErrors "github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/hellofresh/klepto/pkg/config"
	"github.com/hellofresh/klepto/pkg/database"
	"github.com/hellofresh/klepto/pkg/dumper"
	"github.com/hellofresh/klepto/pkg/reader"
)

type (
	textDumper struct {
		reader reader.Reader
		output io.Writer
	}
)

// NewDumper returns a new text dumper implementation.
func NewDumper(output io.Writer, rdr reader.Reader) dumper.Dumper {
	return &textDumper{
		reader: rdr,
		output: output,
	}
}

// Dump executes the dump stream process.
func (d *textDumper) Dump(done chan<- struct{}, cfgTables config.Tables, concurrency int) error {
	tables, err := d.reader.GetTables()
	if err != nil {
		return wErrors.Wrap(err, "failed to get tables")
	}

	structure, err := d.reader.GetStructure()
	if err != nil {
		return wErrors.Wrap(err, "could not get database structure")
	}
	if _, err := io.WriteString(d.output, structure); err != nil {
		return wErrors.Wrap(err, "could not write structure to output")
	}

	for _, tbl := range tables {
		var opts reader.ReadTableOpt
		logger := log.WithField("table", tbl)

		tableConfig := cfgTables.FindByName(tbl)
		if tableConfig == nil {
			logger.Debug("no configuration found for table")
		} else {
			if tableConfig.IgnoreData {
				logger.Debug("ignoring data to dump")
				continue
			}
			opts = reader.NewReadTableOpt(tableConfig)
		}

		// Create read/write chanel
		rowChan := make(chan database.Row)

		go func(tableName string) {
			for {
				row, more := <-rowChan
				if !more {
					done <- struct{}{}
					return
				}

				columnMap, err := d.toSQLColumnMap(row)
				if err != nil {
					logger.WithError(err).Fatal("could not convert value to string")
				}

				insert := sq.Insert(tableName).SetMap(columnMap)
				if _, err := io.WriteString(d.output, sq.DebugSqlizer(insert)); err != nil {
					logger.WithError(err).Error("could not write insert statement to output")
				}
				if _, err := io.WriteString(d.output, "\n"); err != nil {
					logger.WithError(err).Error("could not write new line to output")
				}
			}
		}(tbl)

		if err := d.reader.ReadTable(tbl, rowChan, opts); err != nil {
			log.WithError(err).WithField("table", tbl).Error("error while reading table")
		}
	}

	return nil
}

// Close closes the output stream.
func (d *textDumper) Close() error {
	closer, ok := d.output.(io.WriteCloser)
	if ok {
		if err := closer.Close(); err != nil {
			return wErrors.Wrap(err, "failed to close output stream")
		}
		return nil
	}

	return wErrors.New("unable to close output: wrong closer type")
}

func (d *textDumper) toSQLColumnMap(row database.Row) (map[string]interface{}, error) {
	sqlColumnMap := make(map[string]interface{})

	for column, value := range row {
		strValue, err := d.toSQLStringValue(value)
		if err != nil {
			return sqlColumnMap, err
		}

		sqlColumnMap[column] = fmt.Sprintf("%v", strValue)
	}

	return sqlColumnMap, nil
}

// ResolveType accepts a value and attempts to determine its type
func (d *textDumper) toSQLStringValue(src interface{}) (string, error) {
	switch src.(type) {
	case int64:
		if value, ok := src.(int64); ok {
			return strconv.FormatInt(value, 10), nil
		}
	case float64:
		if value, ok := src.(float64); ok {
			return fmt.Sprintf("%v", value), nil
		}
	case bool:
		if value, ok := src.(bool); ok {
			return strconv.FormatBool(value), nil
		}
	case string:
		if value, ok := src.(string); ok {
			return value, nil
		}
	case []byte:
		// TODO handle blobs?
		if value, ok := src.([]byte); ok {
			return string(value), nil
		}
	case time.Time:
		if value, ok := src.(time.Time); ok {
			return value.String(), nil
		}
	case nil:
		return "NULL", nil
	case *interface{}:
		if src == nil {
			return "NULL", nil
		}
		return d.toSQLStringValue(*(src.(*interface{})))
	default:
		return "", wErrors.New("could not parse type")
	}

	return "", nil
}
