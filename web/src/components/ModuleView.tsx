import { useEffect, useState, type ComponentType, type ReactNode } from 'react';

const loadedModules = new WeakMap<() => Promise<unknown>, unknown>();

export function preloadModule<P extends object>(load: () => Promise<{ default: ComponentType<P> }>) {
  return load().then(module => {
    loadedModules.set(load, module.default);
    return module;
  });
}

// Render resolved route/chart modules immediately, without Suspense's fallback
// reveal throttle. Imports contain public code; callers still gate protected data.
export function ModuleView<P extends object>({ load, componentProps, fallback }: {
  load: () => Promise<{ default: ComponentType<P> }>;
  componentProps: P;
  fallback: ReactNode;
}) {
  const cached = loadedModules.get(load) as ComponentType<P> | undefined;
  const [state, setState] = useState<{ load: typeof load; View: ComponentType<P> | null; failed: boolean; error?: unknown }>({ load, View: null, failed: false });
  useEffect(() => {
    if (cached) return;
    let live = true;
    void preloadModule(load).then(module => {
      if (live) setState({ load, View: module.default, failed: false });
    }, error => {
      if (live) setState({ load, View: null, failed: true, error });
    });
    return () => { live = false; };
  }, [load, cached]);
  if (state.load === load && state.failed) throw state.error ?? new Error('Screen module unavailable');
  const View = cached ?? (state.load === load ? state.View : null);
  return View ? <View {...componentProps} /> : fallback;
}
