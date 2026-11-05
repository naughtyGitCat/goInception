// Copyright 2013 The ql Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSES/QL-LICENSE file.

package session

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hanchuanchuan/goInception/ast"
	"github.com/hanchuanchuan/goInception/model"
)

type oceanbaseVersion struct {
	major, minor, patch, revision int
}

var oceanbaseVersionPattern = regexp.MustCompile(`(?i)oceanbase(?:_[a-z]+)?[-\s]+v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.([0-9]+))?(?:[^0-9.]|$)`)

func parseOceanBaseVersion(value string) oceanbaseVersion {
	parts := oceanbaseVersionPattern.FindStringSubmatch(value)
	if len(parts) == 0 {
		return oceanbaseVersion{}
	}
	var numbers [4]int
	for i := range numbers {
		if parts[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(parts[i+1])
		if err != nil {
			return oceanbaseVersion{}
		}
		numbers[i] = n
	}
	return oceanbaseVersion{numbers[0], numbers[1], numbers[2], numbers[3]}
}

// Prefer the kernel banner; version_comment may contain only a product name.
func (s *session) setServerVersion(version, comment string) {
	s.obVersion = oceanbaseVersion{}
	lower := strings.ToLower(version)
	switch {
	case strings.Contains(lower, "oceanbase") || strings.Contains(strings.ToLower(comment), "oceanbase"):
		s.dbType = DBTypeOceanBase
		s.obVersion = parseOceanBaseVersion(version)
		if s.obVersion.major == 0 {
			s.obVersion = parseOceanBaseVersion(comment)
		}
		s.dbVersion = s.obVersion.major
		if s.obVersion.major == 0 {
			s.appendErrorMsgf("无法解析OceanBase版本号: version=%s, version_comment=%s", version, comment)
		}
		return
	case strings.Contains(lower, "mariadb"):
		s.dbType = DBTypeMariaDB
	case strings.Contains(lower, "tidb"):
		s.dbType = DBTypeTiDB
	default:
		s.dbType = DBTypeMysql
	}
	s.dbVersion = 0
	parts := strings.Split(strings.Split(version, "-")[0], ".")
	if len(parts) != 3 {
		s.appendErrorMsgf("无法解析版本号:%s", version)
		return
	}
	n, err := strconv.Atoi(fmt.Sprintf("%s%02s%02s", parts[0], parts[1], parts[2]))
	if err != nil {
		s.appendErrorMsg(err.Error())
		return
	}
	s.dbVersion = n
}

type oceanbaseAddClass uint8

const (
	oceanbaseAddOnline oceanbaseAddClass = iota
	oceanbaseAddUnknownPosition
	oceanbaseAddStored
	oceanbaseAddAutoIncrement
)

// These release lines document Online middle-column ADD; do not extrapolate
// a numeric threshold to unverified release lines or future major versions.
func (v oceanbaseVersion) supportsAddPosition() bool {
	if v.major == 3 {
		return true
	}
	if v.major != 4 {
		return false
	}
	switch v.minor {
	case 2, 3:
		return v.patch == 5
	case 5:
		return v.patch == 0
	}
	return false
}

func classifyOceanBaseAddColumn(version oceanbaseVersion, column *ast.ColumnDef, position *ast.ColumnPosition) oceanbaseAddClass {
	class := oceanbaseAddOnline
	for _, option := range column.Options {
		if option.Tp == ast.ColumnOptionAutoIncrement && version.major != 3 {
			class = oceanbaseAddAutoIncrement
		} else if option.Tp == ast.ColumnOptionGenerated && option.Stored && version.major != 3 && class < oceanbaseAddStored {
			class = oceanbaseAddStored
		}
	}
	if class != oceanbaseAddOnline || position == nil || position.Tp == ast.ColumnPositionNone {
		return class
	}
	if (position.Tp == ast.ColumnPositionFirst || position.Tp == ast.ColumnPositionAfter) && version.supportsAddPosition() {
		return oceanbaseAddOnline
	}
	return oceanbaseAddUnknownPosition
}

func (s *session) checkPartitionTruncate(t *TableInfo, parts []model.CIStr) {
	for _, part := range parts {
		found := false
		for _, oldPart := range t.Partitions {
			if strings.EqualFold(part.String(), oldPart.PartName) {
				found = true
				break
			}
		}
		if found && s.dbType == DBTypeOceanBase && s.inc.CheckOfflineDDL {
			if s.dbVersion == 3 {
				// Do Nothing, It's OnLine DDL under 3.x
			} else if s.dbVersion == 4 {
				s.appendErrorNo(ER_CANT_TRUNCATE_PARTITION)
				break
			}
		}

		if !found {
			s.appendErrorNo(ErrPartitionNotExisted, part.String())
		}
	}
}

func (s *session) checkAlterPartitionRule(t *TableInfo, opts *ast.PartitionOptions) {
	if opts == nil {
		return
	}

	if s.dbType == DBTypeOceanBase && s.inc.CheckOfflineDDL {
		s.appendErrorMsg(fmt.Sprintf("Can't alter partition rule of table '%s'.", t.Name))
	}
}

func (s *session) checkCharsetChange(charset string, table string) bool {
	if s.inc.CheckOfflineDDL {
		s.appendErrorMsg(fmt.Sprintf("Can't change chartset of table '%s' to '%s'.", table, charset))
		return true
	}
	return false
}
