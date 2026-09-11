const BYTES_PER_GIB = 1024 ** 3

const UNIT_IN_GIB: Record<string, number> = {
  B: 1 / BYTES_PER_GIB,
  KB: 1000 / BYTES_PER_GIB,
  KIB: 1024 / BYTES_PER_GIB,
  MB: 1000 ** 2 / BYTES_PER_GIB,
  MIB: 1024 ** 2 / BYTES_PER_GIB,
  GB: 1000 ** 3 / BYTES_PER_GIB,
  GIB: 1,
  TB: 1000 ** 4 / BYTES_PER_GIB,
  TIB: 1024,
}

export const sizeToGiB = (size: string | number | null | undefined): number => {
  if (typeof size === 'number') {
    return Number.isFinite(size) && size >= 0 ? size / BYTES_PER_GIB : 0
  }

  if (typeof size !== 'string') return 0

  const match = size.trim().match(/^([0-9]+(?:\.[0-9]+)?)\s*([KMGT]?I?B)?$/i)
  if (!match) return 0

  const value = Number(match[1])
  const unit = (match[2] || 'B').toUpperCase()
  return Number.isFinite(value) ? value * (UNIT_IN_GIB[unit] ?? 0) : 0
}

export const formatBytes = (bytes: number): string => {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'

  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** unitIndex
  const precision = unitIndex === 0 ? 0 : 1
  return `${value.toFixed(precision)} ${units[unitIndex]}`
}
