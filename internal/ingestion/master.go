package ingestion

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

type ReferenceShape string

const (
	ShapeIDDescription ReferenceShape = "id_description_objects"
	ShapeStringArray   ReferenceShape = "string_array"
	ShapeEmptyArray    ReferenceShape = "empty_array"
)

type ReferenceItem struct {
	SourceOrdinal     uint64
	Code, Description string
	RawItemPayload    json.RawMessage
	Checksum          [sha256.Size]byte
}

type ReferenceCategory struct {
	Key                                             string
	Shape                                           ReferenceShape
	SourceItemCount, ItemCount, DiscardedBlankCount uint64
	Items                                           []ReferenceItem
	Checksum                                        [sha256.Size]byte
}

type ReferenceCandidate struct {
	Domain                         ReferenceDomain
	Categories                     []ReferenceCategory
	ItemCount, DiscardedBlankCount uint64
	Checksum                       [sha256.Size]byte
}

type MarketingEntity struct {
	ID, Name, LocationName, ActiveStatus, DocumentStatus, SourceTransactionAt string
	RawPayload                                                                json.RawMessage
	Checksum                                                                  [sha256.Size]byte
}

type MarketingCandidate struct {
	Entities []MarketingEntity
	Checksum [sha256.Size]byte
}

func NormalizeReference(domain ReferenceDomain, source map[string]json.RawMessage) (ReferenceCandidate, error) {
	if domain != ReferenceCIF && domain != ReferenceSaving && domain != ReferenceTimeDeposit && domain != ReferenceLoan {
		return ReferenceCandidate{}, fmt.Errorf("unsupported reference domain %q", domain)
	}
	keys := make([]string, 0, len(source))
	for key := range source {
		if strings.TrimSpace(key) == "" || utf8.RuneCountInString(key) > 128 {
			return ReferenceCandidate{}, fmt.Errorf("invalid reference category key")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	candidate := ReferenceCandidate{Domain: domain, Categories: make([]ReferenceCategory, 0, len(keys))}
	for _, key := range keys {
		category, err := normalizeReferenceCategory(key, source[key])
		if err != nil {
			return ReferenceCandidate{}, fmt.Errorf("category %s: %w", key, err)
		}
		candidate.ItemCount += category.ItemCount
		candidate.DiscardedBlankCount += category.DiscardedBlankCount
		candidate.Categories = append(candidate.Categories, category)
	}
	candidate.Checksum = ReferenceDatasetChecksum(candidate.Categories)
	return candidate, nil
}

func normalizeReferenceCategory(key string, raw json.RawMessage) (ReferenceCategory, error) {
	var values []json.RawMessage
	if err := decodeCanonical(raw, &values); err != nil || values == nil {
		return ReferenceCategory{}, fmt.Errorf("value must be an array")
	}
	category := ReferenceCategory{Key: key, SourceItemCount: uint64(len(values))}
	if len(values) == 0 {
		category.Shape = ShapeEmptyArray
		category.Checksum = ReferenceCategoryChecksum(category)
		return category, nil
	}
	first := bytes.TrimSpace(values[0])
	switch first[0] {
	case '{':
		category.Shape = ShapeIDDescription
	case '"':
		category.Shape = ShapeStringArray
	default:
		return ReferenceCategory{}, fmt.Errorf("unsupported item shape at ordinal 0")
	}
	for ordinal, rawItem := range values {
		item, blank, err := normalizeReferenceItem(category.Shape, uint64(ordinal), rawItem)
		if err != nil {
			return ReferenceCategory{}, fmt.Errorf("ordinal %d: %w", ordinal, err)
		}
		if blank {
			category.DiscardedBlankCount++
			continue
		}
		category.Items = append(category.Items, item)
	}
	category.ItemCount = uint64(len(category.Items))
	category.Checksum = ReferenceCategoryChecksum(category)
	return category, nil
}

func normalizeReferenceItem(shape ReferenceShape, ordinal uint64, raw json.RawMessage) (ReferenceItem, bool, error) {
	item := ReferenceItem{SourceOrdinal: ordinal}
	switch shape {
	case ShapeIDDescription:
		var value map[string]json.RawMessage
		if err := decodeCanonical(raw, &value); err != nil || value == nil {
			return item, false, fmt.Errorf("all items must be objects")
		}
		id, ok := value["id"]
		if !ok || json.Unmarshal(id, &item.Code) != nil {
			return item, false, fmt.Errorf("id must be a string")
		}
		descr, ok := value["descr"]
		if !ok || json.Unmarshal(descr, &item.Description) != nil {
			return item, false, fmt.Errorf("descr must be a string")
		}
	case ShapeStringArray:
		if err := json.Unmarshal(raw, &item.Code); err != nil {
			return item, false, fmt.Errorf("all items must be strings")
		}
		item.Description = item.Code
	default:
		return item, false, fmt.Errorf("unsupported source shape %q", shape)
	}
	if strings.TrimSpace(item.Code) == "" {
		return item, true, nil
	}
	if utf8.RuneCountInString(item.Code) > 255 {
		return item, false, fmt.Errorf("id exceeds 255 characters")
	}
	if len(item.Description) > 65535 {
		return item, false, fmt.Errorf("description exceeds 65535 bytes")
	}
	canonical, err := CanonicalJSON(raw)
	if err != nil {
		return item, false, err
	}
	item.RawItemPayload = canonical
	item.Checksum = ReferenceItemChecksum(shape, item.Code, item.Description, canonical)
	return item, false, nil
}

func NormalizeMarketing(source []json.RawMessage) (MarketingCandidate, error) {
	candidate := MarketingCandidate{Entities: make([]MarketingEntity, 0, len(source))}
	seen := make(map[string]struct{}, len(source))
	for ordinal, raw := range source {
		var value map[string]json.RawMessage
		if err := decodeCanonical(raw, &value); err != nil || value == nil {
			return MarketingCandidate{}, fmt.Errorf("marketing ordinal %d must be an object", ordinal)
		}
		entity := MarketingEntity{}
		for _, field := range []struct {
			name   string
			target *string
		}{
			{"id", &entity.ID}, {"nama_marketing", &entity.Name}, {"locationname", &entity.LocationName},
			{"aktif", &entity.ActiveStatus}, {"status_dokumen", &entity.DocumentStatus}, {"tgltransaksi", &entity.SourceTransactionAt},
		} {
			rawField, ok := value[field.name]
			if !ok || json.Unmarshal(rawField, field.target) != nil {
				return MarketingCandidate{}, fmt.Errorf("marketing ordinal %d field %s must be a string", ordinal, field.name)
			}
		}
		if strings.TrimSpace(entity.ID) == "" || utf8.RuneCountInString(entity.ID) > 128 || utf8.RuneCountInString(entity.Name) > 255 || utf8.RuneCountInString(entity.LocationName) > 255 || utf8.RuneCountInString(entity.ActiveStatus) > 64 || utf8.RuneCountInString(entity.DocumentStatus) > 64 || utf8.RuneCountInString(entity.SourceTransactionAt) > 64 {
			return MarketingCandidate{}, fmt.Errorf("marketing ordinal %d has invalid id", ordinal)
		}
		if _, duplicate := seen[entity.ID]; duplicate {
			return MarketingCandidate{}, fmt.Errorf("duplicate marketing id at ordinal %d", ordinal)
		}
		seen[entity.ID] = struct{}{}
		canonical, err := CanonicalJSON(raw)
		if err != nil {
			return MarketingCandidate{}, err
		}
		entity.RawPayload = canonical
		entity.Checksum = MarketingEntityChecksum(entity)
		candidate.Entities = append(candidate.Entities, entity)
	}
	sort.Slice(candidate.Entities, func(i, j int) bool { return candidate.Entities[i].ID < candidate.Entities[j].ID })
	candidate.Checksum = MarketingDatasetChecksum(candidate.Entities)
	return candidate, nil
}

func CanonicalJSON(raw []byte) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode canonical JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("decode canonical JSON: trailing value")
	}
	value, err := normalizeJSONNumbers(value)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical JSON: %w", err)
	}
	return encoded, nil
}

func normalizeJSONNumbers(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		number, err := decimal.NewFromString(typed.String())
		if err != nil {
			return nil, fmt.Errorf("canonicalize JSON number: %w", err)
		}
		return json.RawMessage(number.String()), nil
	case []any:
		for index := range typed {
			normalized, err := normalizeJSONNumbers(typed[index])
			if err != nil {
				return nil, err
			}
			typed[index] = normalized
		}
	case map[string]any:
		for key := range typed {
			normalized, err := normalizeJSONNumbers(typed[key])
			if err != nil {
				return nil, err
			}
			typed[key] = normalized
		}
	}
	return value, nil
}

func decodeCanonical(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

type framedHash struct{ bytes.Buffer }

func (h *framedHash) field(value []byte) {
	_ = binary.Write(&h.Buffer, binary.BigEndian, uint64(len(value)))
	_, _ = h.Write(value)
}
func (h *framedHash) number(value uint64)    { _ = binary.Write(&h.Buffer, binary.BigEndian, value) }
func (h *framedHash) sum() [sha256.Size]byte { return sha256.Sum256(h.Bytes()) }

func ReferenceItemChecksum(shape ReferenceShape, code, description string, raw []byte) [sha256.Size]byte {
	var h framedHash
	h.field([]byte("fincloud-reference-item:v1"))
	h.field([]byte(shape))
	h.field([]byte(code))
	h.field([]byte(description))
	h.field(raw)
	return h.sum()
}
func ReferenceCategoryChecksum(category ReferenceCategory) [sha256.Size]byte {
	var h framedHash
	h.field([]byte("fincloud-reference-category:v1"))
	h.field([]byte(category.Key))
	h.field([]byte(category.Shape))
	h.number(category.SourceItemCount)
	h.number(category.ItemCount)
	h.number(category.DiscardedBlankCount)
	items := append([]ReferenceItem(nil), category.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].SourceOrdinal < items[j].SourceOrdinal })
	for _, item := range items {
		h.number(item.SourceOrdinal)
		h.field(item.Checksum[:])
	}
	return h.sum()
}
func ReferenceDatasetChecksum(categories []ReferenceCategory) [sha256.Size]byte {
	var h framedHash
	h.field([]byte("fincloud-reference-dataset:v1"))
	ordered := append([]ReferenceCategory(nil), categories...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	for _, category := range ordered {
		h.field([]byte(category.Key))
		h.field(category.Checksum[:])
	}
	return h.sum()
}
func MarketingEntityChecksum(entity MarketingEntity) [sha256.Size]byte {
	var h framedHash
	h.field([]byte("fincloud-marketing-entity:v1"))
	for _, value := range []string{entity.ID, entity.Name, entity.LocationName, entity.ActiveStatus, entity.DocumentStatus, entity.SourceTransactionAt} {
		h.field([]byte(value))
	}
	h.field(entity.RawPayload)
	return h.sum()
}
func MarketingDatasetChecksum(entities []MarketingEntity) [sha256.Size]byte {
	var h framedHash
	h.field([]byte("fincloud-marketing-dataset:v1"))
	ordered := append([]MarketingEntity(nil), entities...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, entity := range ordered {
		h.field([]byte(entity.ID))
		h.field(entity.Checksum[:])
	}
	return h.sum()
}
func ChecksumHex(checksum [sha256.Size]byte) string { return hex.EncodeToString(checksum[:]) }
