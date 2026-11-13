package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/netproxy"
)

const (
	defaultIMAPTimeout   = 8 * time.Second
	defaultMaxAliases    = domain.MaxEnabledAliasesPerAccount
	defaultMaxCandidates = 1024
	// Message bodies use a combined FETCH and each batch commits atomically.
	// Small default batches can save progress before exhausting the sync budget.
	defaultMaxIncrementalCandidates = 32
	maxIncrementalCandidates        = 256
	defaultMaxHeaderBytes           = 128 << 10
	defaultMaxMessageBytes          = 100 << 20
	defaultMaxBodyBytes             = 512 << 10
	// Sequence discovery includes one leading/trailing sentinel around a batch.
	maxSequenceFetchMessages = defaultMaxCandidates + 1
)

var (
	ErrAccountDisabled      = errors.New("mail account is disabled")
	ErrAliasAccountMismatch = errors.New("alias does not belong to account")
	ErrInvalidAlias         = errors.New("invalid alias")
	ErrInvalidIMAPConfig    = errors.New("invalid IMAP configuration")
	ErrTooManyAliases       = errors.New("too many aliases")
)

// Fetcher incrementally archives every message after the committed account
// cursor. It never changes upstream flags and never backfills the mailbox when
// a cursor is first created or UIDVALIDITY changes.
type Fetcher struct {
	IMAPTimeout time.Duration
	// ProxyDecrypt is assigned at startup to decrypt saved account proxy endpoints.
	ProxyDecrypt func(string) (string, error)
	MaxAliases   int
	// MaxCandidates bounds the recent UID window established on a reset.
	MaxCandidates            int
	MaxIncrementalCandidates int
	MaxHeaderBytes           int
	MaxMessageBytes          int
	MaxBodyBytes             int
	MaxParsedMessageBytes    int
	ActiveRecentWindow       time.Duration
	// Deprecated: active recovery uses ActiveRecentWindow and UID pagination,
	// rather than truncating the whole window to a fixed message count.
	ActiveRecentCandidates    int
	AllowWeakRecipientHeaders bool
	// ArchiveTempDir receives complete MIME literals before the store publishes
	// them atomically into the archive tree.
	ArchiveTempDir string

	now func() time.Time
}

// NewFetcher returns a fetcher with production defaults. Configuration may
// override the exported limits before the first synchronization.
func NewFetcher() *Fetcher {
	return &Fetcher{
		IMAPTimeout:              defaultIMAPTimeout,
		MaxAliases:               defaultMaxAliases,
		MaxCandidates:            defaultMaxCandidates,
		MaxIncrementalCandidates: defaultMaxIncrementalCandidates,
		MaxHeaderBytes:           defaultMaxHeaderBytes,
		MaxMessageBytes:          defaultMaxMessageBytes,
		MaxBodyBytes:             defaultMaxBodyBytes,
		now:                      time.Now,
	}
}

// FetchIncremental reads one bounded account-level UID window and routes each
// message locally to every matching alias.
func (f *Fetcher) FetchIncremental(
	ctx context.Context,
	account domain.Account,
	password string,
	aliases []domain.Alias,
	previous *domain.IMAPSyncState,
	snapshotPositions map[int64]domain.MailboxSnapshotPosition,
) (domain.MailboxSyncResult, error) {
	return f.fetchArchiveIncremental(
		ctx, account, password, aliases, previous, snapshotPositions, f.settings(),
	)
}

func (f *Fetcher) FetchActiveIncremental(ctx context.Context, account domain.Account, password string, aliases []domain.Alias, previous *domain.IMAPSyncState, positions map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error) {
	settings := f.settings()
	settings.activeRecent = true
	return f.fetchArchiveIncremental(ctx, account, password, aliases, previous, positions, settings)
}

// FetchAliasIncremental searches upstream recipient headers before downloading
// content. Its cursor belongs to this alias, never to the whole account.
func (f *Fetcher) FetchAliasIncremental(ctx context.Context, account domain.Account, password string, alias domain.Alias, knownAliases []domain.Alias, previous *domain.IMAPSyncState, positions map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error) {
	if !alias.Enabled || alias.ID < 1 || alias.AccountID != account.ID {
		return domain.MailboxSyncResult{}, ErrInvalidAlias
	}
	if knownAliases == nil {
		knownAliases = []domain.Alias{alias}
	}
	targetKnown := false
	for _, known := range knownAliases {
		if known.ID == alias.ID {
			if !known.Enabled || known.AccountID != alias.AccountID || known.Address != alias.Address {
				return domain.MailboxSyncResult{}, ErrInvalidAlias
			}
			targetKnown = true
		}
	}
	if !targetKnown {
		return domain.MailboxSyncResult{}, ErrInvalidAlias
	}
	if err := validateLegacySnapshotPositions([]domain.Alias{alias}, positions); err != nil {
		return domain.MailboxSyncResult{}, err
	}
	settings := f.settings()
	settings.targetAliasAddress = alias.Address
	settings.targetAliasID = alias.ID
	return f.fetchArchiveIncremental(ctx, account, password, knownAliases, previous, positions, settings)
}

type fetchSettings struct {
	proxy                     *netproxy.Config
	targetAliasAddress        string
	targetAliasID             int64
	timeout                   time.Duration
	maxAliases                int
	maxCandidates             int
	maxIncrementalCandidates  int
	maxHeaderBytes            int
	maxMessageBytes           int
	maxBodyBytes              int
	maxParsedMessageBytes     int
	allowWeakRecipientHeaders bool
	now                       func() time.Time
	archiveTempDir            string
	activeRecent              bool
	activeRecentWindow        time.Duration
}

func (f *Fetcher) accountSettings(account domain.Account, settings fetchSettings) (fetchSettings, error) {
	if account.Proxy == nil || !account.Proxy.Enabled {
		return settings, nil
	}
	if f == nil || f.ProxyDecrypt == nil {
		return settings, errors.New("IMAP account proxy decryptor is unavailable")
	}
	endpoint, err := f.ProxyDecrypt(account.Proxy.EndpointCiphertext)
	if err != nil {
		return settings, fmt.Errorf("decrypt IMAP account proxy: %w", err)
	}
	upstream := ""
	if account.Proxy.UpstreamCiphertext != "" {
		upstream, err = f.ProxyDecrypt(account.Proxy.UpstreamCiphertext)
		if err != nil {
			return settings, fmt.Errorf("decrypt IMAP upstream proxy: %w", err)
		}
	}
	proxy := netproxy.Config{Endpoint: endpoint, Upstream: upstream}
	if err := proxy.Validate(); err != nil {
		return settings, fmt.Errorf("invalid account proxy settings: %w", err)
	}
	settings.proxy = &proxy
	return settings, nil
}

func (settings fetchSettings) dialContext(ctx context.Context, address string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, settings.timeout)
	defer cancel()
	if settings.proxy != nil {
		return settings.proxy.DialContext(dialCtx, "tcp", address)
	}
	return (&net.Dialer{}).DialContext(dialCtx, "tcp", address)
}

func (f *Fetcher) settings() fetchSettings {
	settings := fetchSettings{
		timeout:                  defaultIMAPTimeout,
		maxAliases:               defaultMaxAliases,
		maxCandidates:            defaultMaxCandidates,
		maxIncrementalCandidates: defaultMaxIncrementalCandidates,
		maxHeaderBytes:           defaultMaxHeaderBytes,
		maxMessageBytes:          defaultMaxMessageBytes,
		maxBodyBytes:             defaultMaxBodyBytes,
		now:                      time.Now,
		activeRecentWindow:       15 * time.Minute,
	}
	if f == nil {
		settings.maxParsedMessageBytes = settings.maxBodyBytes + defaultMetadataResultBytes
		return settings
	}
	if f.IMAPTimeout > 0 {
		settings.timeout = f.IMAPTimeout
	}
	if f.MaxAliases > 0 {
		settings.maxAliases = min(f.MaxAliases, domain.MaxEnabledAliasesPerAccount)
	}
	if f.MaxCandidates > 0 {
		settings.maxCandidates = min(f.MaxCandidates, defaultMaxCandidates)
	}
	if f.MaxIncrementalCandidates > 0 {
		settings.maxIncrementalCandidates = min(f.MaxIncrementalCandidates, maxIncrementalCandidates)
	}
	if f.MaxHeaderBytes > 0 {
		settings.maxHeaderBytes = f.MaxHeaderBytes
	}
	if f.MaxMessageBytes > 0 {
		settings.maxMessageBytes = min(f.MaxMessageBytes, defaultMaxMessageBytes)
	}
	if f.MaxBodyBytes > 0 {
		settings.maxBodyBytes = f.MaxBodyBytes
	}
	settings.maxParsedMessageBytes = settings.maxBodyBytes + defaultMetadataResultBytes
	if f.MaxParsedMessageBytes > 0 {
		settings.maxParsedMessageBytes = f.MaxParsedMessageBytes
	}
	if settings.maxParsedMessageBytes < parsedMessageBaseBytes {
		settings.maxParsedMessageBytes = parsedMessageBaseBytes
	}
	settings.allowWeakRecipientHeaders = f.AllowWeakRecipientHeaders
	settings.archiveTempDir = strings.TrimSpace(f.ArchiveTempDir)
	if f.ActiveRecentWindow > 0 {
		settings.activeRecentWindow = f.ActiveRecentWindow
	}
	if f.now != nil {
		settings.now = f.now
	}
	return settings
}

func prepareAliases(account domain.Account, aliases []domain.Alias, maxAliases int) (map[string][]int64, error) {
	byAddress := make(map[string][]int64)
	seenIDs := make(map[int64]struct{})
	// The fixed capacity belongs to the iCloud Hide My Email workflow. Custom
	// mailboxes deliberately have no cumulative alias limit, so their complete
	// enabled set must remain routable after it grows beyond that capacity.
	limitEnabledAliases := domain.NormalizeMailboxType(account.MailboxType) != domain.MailboxTypeCustom
	for _, alias := range aliases {
		if !alias.Enabled {
			continue
		}
		if alias.AccountID != account.ID {
			return nil, fmt.Errorf("%w: alias %d", ErrAliasAccountMismatch, alias.ID)
		}
		if alias.ID <= 0 {
			return nil, fmt.Errorf("%w: missing ID", ErrInvalidAlias)
		}
		if _, exists := seenIDs[alias.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate ID %d", ErrInvalidAlias, alias.ID)
		}
		if limitEnabledAliases && maxAliases > 0 && len(seenIDs) >= maxAliases {
			return nil, ErrTooManyAliases
		}
		seenIDs[alias.ID] = struct{}{}
		address, ok := normalizeAliasAddress(alias.Address)
		if !ok {
			return nil, fmt.Errorf("%w: alias %d address", ErrInvalidAlias, alias.ID)
		}
		byAddress[address] = append(byAddress[address], alias.ID)
	}
	return byAddress, nil
}

func accountEndpoint(account domain.Account) (host, address, username string, err error) {
	if strings.TrimSpace(account.IMAPHost) == "" {
		account.IMAPHost = domain.DefaultIMAPHost
	}
	if account.IMAPPort == 0 {
		account.IMAPPort = domain.DefaultIMAPPort
	}
	var port int
	host, port, err = domain.NormalizeIMAPEndpoint(account.IMAPHost, account.IMAPPort)
	if err != nil {
		return "", "", "", fmt.Errorf("%w: %v", ErrInvalidIMAPConfig, err)
	}
	username = strings.TrimSpace(account.IMAPUsername)
	if username == "" {
		username = strings.TrimSpace(account.Email)
	}
	if username == "" {
		return "", "", "", fmt.Errorf("%w: empty username", ErrInvalidIMAPConfig)
	}
	return host, net.JoinHostPort(host, strconv.Itoa(port)), username, nil
}

func validateIMAPAccount(account domain.Account, password string) error {
	if !account.Enabled {
		return ErrAccountDisabled
	}
	if password == "" {
		return fmt.Errorf("%w: empty password", ErrInvalidIMAPConfig)
	}
	return nil
}
