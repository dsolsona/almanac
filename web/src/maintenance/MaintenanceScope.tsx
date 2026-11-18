import { useEffect, useState } from 'react';
import { Button } from '../components/ui/index.ts';
import { sloClient } from '../slos/client.ts';

function ScopeName({ id, links }: { id: string; links: boolean }) {
  const [name, setName] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    sloClient.get(id, controller.signal).then(result => {
      if (!controller.signal.aborted) setName(result.value.display_name);
    }).catch(() => {});
    return () => controller.abort();
  }, [id]);
  return links ? <a href={`/slos/${id}`} data-app-route>{name || id}</a> : <span>{name || id}</span>;
}

export function MaintenanceScope({ ids, links = false }: { ids: string[]; links?: boolean }) {
  const [visible, setVisible] = useState(10);
  return <>
    <ul>{ids.slice(0, visible).map(id => <li key={id}><ScopeName id={id} links={links} /></li>)}</ul>
    {ids.length > visible && <Button variant="ghost" onClick={() => setVisible(value => value + 10)}>Show more SLOs</Button>}
  </>;
}
