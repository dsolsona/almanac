import { useEffect, useState } from 'react';
import { Button } from '../components/ui/index.ts';
import type { MaintenanceSelector } from './client.ts';
import { scopeIDs, scopeLabels, scopeOption, type SelectedKind } from './ScopePicker.tsx';

function ScopeName({ id, kind, links }: { id: string; kind: SelectedKind; links: boolean }) {
  const [name, setName] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    scopeOption(kind, id, controller.signal).then(option => {
      if (!controller.signal.aborted) setName(option.label);
    }).catch(() => {});
    return () => controller.abort();
  }, [id, kind]);
  return links ? <a href={`/${kind}/${id}`} data-app-route>{name || id}</a> : <span>{name || id}</span>;
}

export function MaintenanceScope({ scope, links = false }: { scope: MaintenanceSelector; links?: boolean }) {
  const [visible, setVisible] = useState(10);
  const ids = scopeIDs(scope);
  if (scope.kind === 'all_services') return <>{scopeLabels.all_services}</>;
  const kind = scope.kind;

  return <>
    <p>{scopeLabels[kind]} · {ids.length}</p>
    <ul>{ids.slice(0, visible).map(id => <li key={id}><ScopeName id={id} kind={kind} links={links} /></li>)}</ul>
    {ids.length > visible && <Button variant="ghost" onClick={() => setVisible(value => value + 10)}>Show more {kind === 'services' ? 'services' : 'SLOs'}</Button>}
  </>;
}
