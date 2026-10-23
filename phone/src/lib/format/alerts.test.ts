import { reasonText } from './alerts';

describe('reasonText', () => {
  it('labels the peers skip reason', () => {
    expect(reasonText('peers')).toBe('talking to another session (peers)');
  });
  it('falls back to spaced words and empty', () => {
    expect(reasonText('rule_off')).toBe('rule is off');
    expect(reasonText('some_new_reason')).toBe('some new reason');
    expect(reasonText(null)).toBe('');
  });
});
