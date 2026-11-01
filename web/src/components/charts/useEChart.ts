import { useEffect, useRef, type DependencyList } from 'react'
import * as echarts from 'echarts/core'
import type { EChartsCoreOption, ECharts } from 'echarts/core'

/**
 * Owns one ECharts instance for the lifetime of the component. Data and theme changes
 * call setOption on the same instance instead of disposing and re-creating it, so a
 * periodic refresh redraws in place without flashing or re-measuring the container.
 */
export function useEChart(build: () => EChartsCoreOption, deps: DependencyList, onReady?: () => void) {
  const container = useRef<HTMLDivElement>(null), chart = useRef<ECharts>(undefined), builder = useRef(build)
  const ready = useRef(onReady)
  ready.current = onReady
  // Theme changes re-run the latest builder; keeping it in a ref avoids re-subscribing observers.
  useEffect(() => { builder.current = build })
  useEffect(() => {
    const element = container.current
    if (!element) return
    const instance = echarts.init(element, undefined, { renderer: 'svg' })
    chart.current = instance
    element.dataset.chartReady = 'false'
    instance.on('finished', () => { element.dataset.chartReady = 'true'; ready.current?.() })
    instance.setOption(builder.current(), { notMerge: true, lazyUpdate: true })
    const resize = new ResizeObserver(() => instance.resize()); resize.observe(element)
    const theme = new MutationObserver(() => { element.dataset.chartReady = 'false'; instance.setOption(builder.current(), { notMerge: true, lazyUpdate: true }) })
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'data-theme'] })
    return () => { resize.disconnect(); theme.disconnect(); instance.dispose(); chart.current = undefined }
  }, [])
  const mounted = useRef(false)
  useEffect(() => {
    // The mount effect above already rendered the first option.
    if (!mounted.current) { mounted.current = true; return }
    if (container.current) container.current.dataset.chartReady = 'false'
    chart.current?.setOption(build(), { notMerge: true, lazyUpdate: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
  return container
}

export const cssColor = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()

