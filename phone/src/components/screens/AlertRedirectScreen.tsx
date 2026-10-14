// /alerts/<id>: a private push links here. Load the alert and replace this route with its deep
// link (push.md 5.8). Never loops back to itself.
import { router } from 'expo-router';
import { useEffect, useState } from 'react';
import { hashToRoute, routeToHref } from '../../lib/links';
import { useHubApi } from '../hub-api';
import { Btn, Calm, Page } from '../ui';

export default function AlertRedirectScreen({ id }: { id: string }) {
  const api = useHubApi();
  const [error, setError] = useState('');
  const client = api.client;

  useEffect(() => {
    if (!client) return;
    let live = true;
    client.alert(id).then(
      (a) => {
        if (!live) return;
        const route = a.url ? hashToRoute(a.url) : null;
        router.replace((route && route.name !== 'alert' ? routeToHref(route) : '/') as never);
      },
      (e: unknown) => {
        if (live) setError((e as { status?: number }).status === 404 ? 'That alert no longer exists.' : 'Could not load that alert.');
      },
    );
    return () => {
      live = false;
    };
  }, [client, id]);

  return (
    <Page>
      {error ? (
        <>
          <Calm>{error}</Calm>
          <Btn label="Go to Now" onPress={() => router.replace('/' as never)} />
        </>
      ) : (
        <Calm>Opening…</Calm>
      )}
    </Page>
  );
}
