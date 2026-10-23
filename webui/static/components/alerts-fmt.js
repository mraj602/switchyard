// Labels of the reasons an alert was skipped, cancelled or suppressed (push.md 3). Pure, so
// node can test it (alerts-fmt.test.cjs): no imports.

export const REASONS = {
  remote_control: 'the Claude app already notifies', rule_off: 'rule is off', stale: 'too old to send',
  superseded: 'replaced by a later alert', baseline: 'existing at first start', answered: 'you answered first',
  peers: 'talking to another session (peers)', ended: 'session ended', recovered: 'recovered',
  reset_passed: 'window reset', seen: 'seen',
};

export const reasonText = (r) => REASONS[r] || (r ? String(r).replace(/_/g, ' ') : '');
