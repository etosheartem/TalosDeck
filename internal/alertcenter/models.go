package alertcenter

import (
	"context"
	"time"
)

type Store interface {
	GetSecret(context.Context, string, string, string) ([]byte, error)
	PutSecret(context.Context, string, string, string, []byte) error
}
type Sender interface {
	Send(context.Context, ChannelSecret, Notification) (DeliveryOutcome, error)
}
type DeliveryOutcome struct {
	Uncertain  bool
	RetryAfter time.Duration
}
type ChannelSecret struct {
	ID     string
	Type   string
	Config map[string]string
}
type Notification struct {
	ID         string    `json:"id"`
	ClusterID  string    `json:"clusterId"`
	AlertID    string    `json:"alertId"`
	State      string    `json:"state"`
	Severity   string    `json:"severity"`
	Title      string    `json:"title"`
	Details    string    `json:"details"`
	Node       string    `json:"node,omitempty"`
	RuleID     string    `json:"ruleId"`
	OccurredAt time.Time `json:"occurredAt"`
}
type Observation struct {
	Event           bool
	RuleID          string
	ResourceID      string
	Node            string
	Component       string
	Known           bool
	Active          bool
	Severity        string
	Title           string
	Details         string
	SuggestedAction string
	OccurredAt      time.Time
}
type Alert struct {
	Event           bool       `json:"event,omitempty"`
	ID              string     `json:"id"`
	ClusterID       string     `json:"clusterId"`
	Fingerprint     string     `json:"fingerprint"`
	RuleID          string     `json:"ruleId"`
	ResourceID      string     `json:"resourceId"`
	Node            string     `json:"node,omitempty"`
	Component       string     `json:"component"`
	Severity        string     `json:"severity"`
	State           string     `json:"state"`
	Title           string     `json:"title"`
	Details         string     `json:"details"`
	SuggestedAction string     `json:"suggestedAction,omitempty"`
	FirstSeen       time.Time  `json:"firstSeen"`
	LastSeen        time.Time  `json:"lastSeen"`
	LastChanged     time.Time  `json:"lastChanged"`
	ResolvedAt      *time.Time `json:"resolvedAt,omitempty"`
	Occurrences     int        `json:"occurrences"`
	Transition      int        `json:"transition"`
	Observation     string     `json:"observation"`
	SilencedUntil   *time.Time `json:"silencedUntil,omitempty"`
}
type Silence struct {
	ID        string    `json:"id"`
	RuleID    string    `json:"ruleId,omitempty"`
	Node      string    `json:"node,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}
type Channel struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	RecipientUser string    `json:"recipientUser,omitempty"`
	Enabled       bool      `json:"enabled"`
	Configured    bool      `json:"configured"`
	Generation    int       `json:"generation"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
type ChannelInput struct {
	ID            string            `json:"id,omitempty"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	RecipientUser string            `json:"recipientUser,omitempty"`
	Enabled       bool              `json:"enabled"`
	Generation    int               `json:"generation"`
	Config        map[string]string `json:"config"`
}
type Route struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	MinSeverity   string   `json:"minSeverity"`
	RuleIDs       []string `json:"ruleIds,omitempty"`
	NodeIDs       []string `json:"nodeIds,omitempty"`
	ChannelIDs    []string `json:"channelIds"`
	SendResolved  bool     `json:"sendResolved"`
	RepeatMinutes int      `json:"repeatMinutes,omitempty"`
}
type RoutesConfig struct {
	Revision int     `json:"revision"`
	Routes   []Route `json:"routes"`
}
type Delivery struct {
	ID                string       `json:"id"`
	AlertID           string       `json:"alertId"`
	Transition        int          `json:"transition"`
	ChannelID         string       `json:"channelId"`
	ChannelGeneration int          `json:"channelGeneration"`
	State             string       `json:"state"`
	Attempts          int          `json:"attempts"`
	NextAttemptAt     time.Time    `json:"nextAttemptAt"`
	CreatedAt         time.Time    `json:"createdAt"`
	LastAttemptAt     *time.Time   `json:"lastAttemptAt,omitempty"`
	DeliveredAt       *time.Time   `json:"deliveredAt,omitempty"`
	ErrorCode         string       `json:"errorCode,omitempty"`
	Notification      Notification `json:"-"`
	RepeatSlot        int64        `json:"-"`
	Test              bool         `json:"-"`
}
type Summary struct {
	Active   int `json:"active"`
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Silenced int `json:"silenced"`
	Stale    int `json:"stale"`
}
type Snapshot struct {
	Alerts      []Alert   `json:"alerts"`
	Summary     Summary   `json:"summary"`
	Health      string    `json:"health"`
	LastCheckAt time.Time `json:"lastCheckAt"`
}
