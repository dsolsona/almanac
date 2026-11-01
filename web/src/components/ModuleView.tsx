import { useEffect, useState, type ComponentType, type ReactNode } from 'react';

// Render resolved route/chart modules immediately, without Suspense's fallback
// reveal throttle. Imports contain public code; callers still gate protected data.
export function ModuleView<P extends object>({ load, componentProps, fallback }: {
  load: () => Promise<{ default: ComponentType<P> }>;
  componentProps: P;
  fallback: ReactNode;
}) {
  const [state, setState] = useState<{ load: typeof load; View: ComponentType<P> | null; failed: boolean; error?: unknown }>({ load, View: null, failed: false });
  useEffect(() => {
    let live = true;
    void load().then(module => {
      if (live) setState({ load, View: module.default, failed: false });
    }, error => {
      if (live) setState({ load, View: null, failed: true, error });
    });
    return () => { live = false; };
  }, [load]);
  if (state.load !== load) return fallback;
  if (state.failed) throw state.error ?? new Error('Screen module unavailable');
  const View = state.View;
  return View ? <View {...componentProps} /> : fallback;
}
