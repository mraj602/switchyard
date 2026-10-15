// Analytics (More; analytics.md): tokens and API-equivalent cost over 7, 30 or 90 days, one
// account or all: totals, daily cost bars, by model, by project and limit hits. Read-only. The
// dollar figure is what the same tokens cost at API list prices, a yardstick, not a bill. The
// weekday by hour heatmap is on the board.
import { useEffect, useState } from 'react';
import { ScrollView, Text, View } from 'react-native';
import type { AnalyticsReplies } from '../../contract/api';
import { barPercents, cacheHit, dateTime, hitText, plural, seatName, shortDay, tokens, tokenSum, usd } from '../../lib/format';
import { useHubApi } from '../hub-api';
import { useTheme } from '../theme';
import { Calm, Card, Chip, Muted, Page, Section } from '../ui';

const RANGES = [7, 30, 90] as const;

interface Data {
  daily: AnalyticsReplies['daily'];
  models: AnalyticsReplies['models'];
  projects: AnalyticsReplies['projects'];
  hits: AnalyticsReplies['hits'];
}

function Tile({ label, value, sub, testID }: { label: string; value: string; sub?: string; testID: string }) {
  const t = useTheme();
  return (
    <Card style={{ flexGrow: 1, flexBasis: '45%' }}>
      <Muted>{label}</Muted>
      <Text testID={testID} style={{ color: t.text, fontSize: 22, fontWeight: '600' }}>{value}</Text>
      {sub ? <Muted>{sub}</Muted> : null}
    </Card>
  );
}

/** Rows with a bar behind the cost; the widest is the dearest. */
function Rows({ items, testID }: { items: { key: string; name: string; sub: string; usd: number }[]; testID: string }) {
  const t = useTheme();
  const widths = barPercents(items.map((i) => i.usd));
  return (
    <Card style={{ padding: 0, overflow: 'hidden' }}>
      {items.map((i, n) => (
        <View key={i.key} testID={`${testID}-${n}`} style={{ borderTopWidth: n ? 1 : 0, borderTopColor: t.line }}>
          <View style={{ position: 'absolute', left: 0, top: 0, bottom: 0, width: `${widths[n] ?? 0}%`, backgroundColor: t.track }} />
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', gap: 12, padding: 12 }}>
            <View style={{ flexShrink: 1 }}>
              <Text style={{ color: t.text, fontWeight: '600' }}>{i.name}</Text>
              <Muted>{i.sub}</Muted>
            </View>
            <Text style={{ color: t.text, fontWeight: '600' }}>{usd(i.usd)}</Text>
          </View>
        </View>
      ))}
    </Card>
  );
}

export default function AnalyticsScreen() {
  const api = useHubApi();
  const t = useTheme();
  const client = api.client;
  const [days, setDays] = useState<number>(30);
  const [seat, setSeat] = useState('');
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!client) return;
    let live = true;
    const q = { days, seat: seat || undefined };
    void Promise.all([client.analytics('daily', q), client.analytics('models', q), client.analytics('projects', q), client.analytics('hits', q)])
      .then(([daily, models, projects, hits]) => {
        if (!live) return;
        setData({ daily, models, projects, hits });
        setError(null);
      })
      .catch((e: unknown) => {
        if (live) setError(e instanceof Error ? e.message : 'Could not load analytics');
      });
    return () => {
      live = false;
    };
  }, [client, days, seat]);

  const seats = (api.data.overview?.seats ?? []).filter((s) => !s.hidden);
  const filters = (
    <View style={{ gap: 8 }}>
      <View style={{ flexDirection: 'row', gap: 8 }}>
        {RANGES.map((n) => (
          <Chip key={n} testID={`range-${n}`} label={`${n} days`} on={days === n} onPress={() => setDays(n)} />
        ))}
      </View>
      <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: 8 }}>
        <Chip testID="seat-all" label="All accounts" on={seat === ''} onPress={() => setSeat('')} />
        {seats.map((s) => (
          <Chip key={s.id} testID={`seat-${s.id}`} label={seatName(s)} on={seat === s.id} onPress={() => setSeat(s.id)} />
        ))}
        <Chip testID="seat-unattributed" label="Unattributed" on={seat === 'unattributed'} onPress={() => setSeat('unattributed')} />
      </ScrollView>
    </View>
  );

  if (!data) {
    return (
      <Page title="Analytics">
        {filters}
        <Calm>{error ?? (api.data.offline ? 'Offline.' : 'Loading…')}</Calm>
      </Page>
    );
  }

  const { daily, models, projects, hits } = data;
  const tot = daily.totals;
  const hit = cacheHit(tot);
  const heights = barPercents(daily.days.map((d) => d.api_usd));
  return (
    <Page title="Analytics" meta={`${shortDay(daily.from)} to ${shortDay(daily.to)} · ${daily.tz}`}>
      {filters}
      {error ? <Muted>{error}</Muted> : null}
      <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 8 }}>
        <Tile testID="total-usd" label="API-equivalent cost" value={usd(tot.api_usd)} sub={tot.unpriced_replies ? `${plural(tot.unpriced_replies, 'reply', 'replies')} not priced` : undefined} />
        <Tile testID="total-tokens" label="Tokens" value={tokens(tokenSum(tot))} sub={`${tokens(tot.input)} in · ${tokens(tot.output)} out`} />
        <Tile testID="total-replies" label="Replies" value={String(tot.replies)} />
        <Tile testID="total-cache" label="Cache reads" value={hit == null ? '–' : `${Math.round(hit * 100)}%`} sub="of input" />
      </View>

      <Section title="Cost per day">
        <Card>
          <View
            testID="day-bars"
            accessibilityRole="image"
            accessibilityLabel={`Daily API-equivalent cost, ${shortDay(daily.from)} to ${shortDay(daily.to)}, ${usd(tot.api_usd)} in all`}
            style={{ flexDirection: 'row', alignItems: 'flex-end', height: 120, gap: 2 }}
          >
            {daily.days.map((d, i) => (
              <View key={d.day} style={{ flex: 1, height: `${heights[i] ?? 0}%`, backgroundColor: t.accent, borderTopLeftRadius: 2, borderTopRightRadius: 2 }} />
            ))}
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
            <Muted>{shortDay(daily.from)}</Muted>
            <Muted>{shortDay(daily.to)}</Muted>
          </View>
        </Card>
      </Section>

      <Section title="By model">
        {models.models.length ? (
          <Rows
            testID="model"
            items={models.models.map((m) => ({
              key: m.model || '-',
              name: m.label,
              sub: `${tokens(tokenSum(m))} tokens · ${plural(m.replies, 'reply', 'replies')}${m.priced ? '' : ' · no price'}`,
              usd: m.api_usd,
            }))}
          />
        ) : (
          <Muted>No token data in this range.</Muted>
        )}
      </Section>

      <Section title="By project">
        {projects.projects.length ? (
          <Rows
            testID="project"
            items={projects.projects.map((p) => ({
              key: p.project || '-',
              name: p.name,
              sub: `${plural(p.sessions, 'session')} · ${tokens(tokenSum(p))} tokens`,
              usd: p.api_usd,
            }))}
          />
        ) : (
          <Muted>No token data in this range.</Muted>
        )}
      </Section>

      <Section title="Limit hits">
        {hits.hits.length ? (
          <Card style={{ gap: 10 }}>
            {hits.hits
              .slice()
              .reverse()
              .map((h) => (
                <View key={`${h.session_id}-${h.ts}`} testID="hit">
                  <Text style={{ color: t.text, fontWeight: '600' }}>{hitText(h)}</Text>
                  <Muted>{`${h.seat_name} · ${dateTime(h.ts)}`}</Muted>
                </View>
              ))}
          </Card>
        ) : (
          <Muted>No limit hits in this range.</Muted>
        )}
      </Section>
      <Muted>Cost is the API list price of the same tokens, a yardstick, not a bill. Days follow the hub time zone.</Muted>
    </Page>
  );
}
