package fincloud

import (
	"context"
	"encoding/json"
	"net/url"
)

// SavingBlockDetail is the current block state of one saving account. Only
// the raw result is retained; the mapper validates norekening and persists
// nothing but the datablockopen items.
type SavingBlockDetail struct {
	RawPayload json.RawMessage `json:"-"`
}

// FetchBlockedSavingAccounts discovers saving accounts that currently carry
// block data. The listing is discovery-only: just row.id is used.
func (c *Client) FetchBlockedSavingAccounts(ctx context.Context) ([]string, error) {
	return c.fetchAccountList(ctx, "fetch blocked saving accounts", "/cs/inquiry/block_savings/cari", url.Values{
		"cabang": {"ALL"}, "datamutasi": {"false"}, "pagenumber": {"0"}, "pagesize": {"999999999999"},
	}, false)
}

func (c *Client) FetchSavingBlockDetail(ctx context.Context, accountNo string) (*SavingBlockDetail, error) {
	return fetchDetail[SavingBlockDetail](ctx, c, "fetch saving block detail", "/cs/inquiry/block_savings/tabungan", "id", accountNo)
}
