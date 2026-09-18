package mail

import (
	"errors"
	"strings"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"icloud-api/internal/domain"
)

const demandAliasUIDWindow = 4096

func aliasRecipientSearch(address, host string) imap.SearchCriteria {
	fields := recipientHeaderFieldsForFetch()
	if host == domain.DefaultIMAPHost {
		// iCloud returns NO [UNAVAILABLE] for SEARCH on headers such as
		// X-Original-To. Search its HME routing header and indexed recipients;
		// the full delivery-header classifier still checks every candidate.
		fields = []string{icloudHMEHeaderField, "To", "Cc"}
	}
	values := aliasRecipientSearchValues(address)
	criteria := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: fields[0], Value: values[0]}}}
	for _, value := range values {
		for _, field := range fields {
			if value == values[0] && field == fields[0] {
				continue
			}
			next := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: field, Value: value}}}
			criteria = imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{criteria, next}}}
		}
	}
	return criteria
}

// aliasRecipientSearchValues adds only the local-part plus prefix needed to
// discover arbitrary tagged deliveries. It is a candidate prefilter: strict
// header classification still requires either the full registered address or
// the same-domain registered root before any message body is downloaded.
func aliasRecipientSearchValues(address string) []string {
	values := []string{address}
	local, _, hasDomain := strings.Cut(address, "@")
	if plus := strings.IndexByte(local, '+'); hasDomain && plus < 0 && local != "" {
		values = append(values, local+"+")
	}
	return values
}

func discoverAliasArchiveUIDs(client *imapclient.Client, lastUID, upperUID uint32, address, host string, limit int) ([]uint32, bool, uint32, error) {
	if limit < 1 || lastUID > upperUID {
		return nil, false, lastUID, errors.New("invalid alias search window")
	}
	if lastUID == upperUID {
		return nil, false, upperUID, nil
	}
	end := upperUID
	if uint64(upperUID)-uint64(lastUID) > demandAliasUIDWindow {
		end = lastUID + demandAliasUIDWindow
	}
	set := imap.UIDSet{}
	set.AddRange(imap.UID(lastUID+1), imap.UID(end))
	criteria := aliasRecipientSearch(address, host)
	criteria.UID = []imap.UIDSet{set}
	data, err := client.UIDSearch(&criteria, nil).Wait()
	if err != nil {
		return nil, false, lastUID, err
	}
	uids, err := validateArchiveUIDSearch(data.AllUIDs(), lastUID, end)
	if err != nil {
		return nil, false, lastUID, err
	}
	if len(uids) > limit {
		return uids[:limit], true, uids[limit-1], nil
	}
	return uids, end < upperUID, end, nil
}
