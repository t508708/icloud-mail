package mail

import (
	"errors"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const demandAliasUIDWindow = 4096

func aliasRecipientSearch(address string) imap.SearchCriteria {
	fields := recipientHeaderFieldsForFetch()
	criteria := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: fields[0], Value: address}}}
	for _, field := range fields[1:] {
		next := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: field, Value: address}}}
		criteria = imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{criteria, next}}}
	}
	return criteria
}

func discoverAliasArchiveUIDs(client *imapclient.Client, lastUID, upperUID uint32, address string, limit int) ([]uint32, bool, uint32, error) {
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
	criteria := aliasRecipientSearch(address)
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
