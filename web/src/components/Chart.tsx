import { useEffect, useRef } from 'react'
import uPlot from 'uplot'

// Thin uPlot lifecycle wrapper: create on mount, setData on data change,
// recreate when options change, resize with the container, destroy on
// unmount. Height is fixed by the caller; width tracks the container.
export default function Chart({
  options,
  data,
}: {
  options: Omit<uPlot.Options, 'width'>
  data: uPlot.AlignedData
}) {
  const hostRef = useRef<HTMLDivElement>(null)
  const plotRef = useRef<uPlot | null>(null)
  const dataRef = useRef(data)
  dataRef.current = data

  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    const plot = new uPlot(
      { ...options, width: Math.max(host.clientWidth, 200) },
      dataRef.current,
      host,
    )
    plotRef.current = plot
    const ro = new ResizeObserver(() => {
      plot.setSize({ width: Math.max(host.clientWidth, 200), height: options.height })
    })
    ro.observe(host)
    return () => {
      ro.disconnect()
      plot.destroy()
      plotRef.current = null
    }
  }, [options])

  useEffect(() => {
    plotRef.current?.setData(data)
  }, [data])

  return <div ref={hostRef} className="chart-host" />
}
