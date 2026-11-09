package sqlvalue

import (
	"database/sql"
	"strconv"
	"strings"
)

func BoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func Int64Arg(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func Int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func IDList(value string) []int64 {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(part, 10, 64)
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}
