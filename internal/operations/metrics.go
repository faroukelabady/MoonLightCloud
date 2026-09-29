package operations

import (
	"sync/atomic"
)

// Metrics are low-cardinality operational counters. No labels carry
// incident, device, notification, run, or recipient identity — only
// rule/severity/action/result aggregates plus gauge queries.
type Metrics struct {
	openedTotal        atomic.Int64
	resolvedTotal      atomic.Int64
	byRuleOpened       map[string]*atomic.Int64
	byRuleResolved     map[string]*atomic.Int64
	deliveriesEnqueued atomic.Int64
	deliveriesBlocked  atomic.Int64
	recoveryTotal      atomic.Int64
	mu                 chan struct{}
}

func NewMetrics() *Metrics {
	return &Metrics{mu: make(chan struct{}, 1)}
}

func (m *Metrics) opened(rule string) {
	m.openedTotal.Add(1)
	m.byRule(rule, true).Add(1)
}

func (m *Metrics) resolved(rule string) {
	m.resolvedTotal.Add(1)
	m.byRule(rule, false).Add(1)
}

func (m *Metrics) byRule(rule string, open bool) *atomic.Int64 {
	// Single-flight map access; detector ticks are bounded and infrequent.
	m.mu <- struct{}{}
	defer func() { <-m.mu }()
	target := m.byRuleOpened
	if !open {
		target = m.byRuleResolved
	}
	if target == nil {
		if open {
			m.byRuleOpened = map[string]*atomic.Int64{}
			target = m.byRuleOpened
		} else {
			m.byRuleResolved = map[string]*atomic.Int64{}
			target = m.byRuleResolved
		}
	}
	c, ok := target[rule]
	if !ok {
		c = &atomic.Int64{}
		target[rule] = c
	}
	return c
}

// Snapshot renders aggregate counters for the summary endpoint.
func (m *Metrics) Snapshot() map[string]int64 {
	out := map[string]int64{
		"incidents_opened_total":    m.openedTotal.Load(),
		"incidents_resolved_total":  m.resolvedTotal.Load(),
		"alert_deliveries_enqueued": m.deliveriesEnqueued.Load(),
		"alert_deliveries_blocked":  m.deliveriesBlocked.Load(),
		"recovery_actions_total":    m.recoveryTotal.Load(),
	}
	return out
}
