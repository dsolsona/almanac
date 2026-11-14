// Presentation values only. Backend validation remains authoritative at submission.
export type AlertConditionDraft = { id: string; shortSeconds: number; longSeconds: number; threshold: string };
export type AlertSeverity = 'low' | 'medium' | 'high';
export type AlertPolicyDraft = { name: string; sloId: string; severity: AlertSeverity; conditions: AlertConditionDraft[]; confirmNonThirtyDayPresets: boolean };

function decimal(value: string): { numerator: bigint; denominator: bigint } | null {
  if (!/^\d+(?:\.\d+)?$/.test(value)) return null;
  const [whole, fraction = ''] = value.split('.');
  return { numerator: BigInt(whole! + fraction), denominator: 10n ** BigInt(fraction.length) };
}
export function normalizeAlertThreshold(value: string): string | null {
  const trimmed = value.trim(), parts = decimal(trimmed);
  if (!parts || parts.numerator <= 0n) return null;
  const [whole, fraction = ''] = trimmed.split('.');
  const tail = fraction.replace(/0+$/, '');
  return `${BigInt(whole!)}${tail ? `.${tail}` : ''}`;
}
export function validateAlertThreshold(value: string, target?: string): string | undefined {
  const threshold = decimal(value.trim());
  if (!threshold || threshold.numerator <= 0n) return 'Enter a positive burn threshold, such as 14.4.';
  if (target !== undefined) {
    const objective = decimal(target);
    if (!objective || objective.numerator >= objective.denominator) return 'Choose an SLO with a target from 0% to below 100%.';
    if (threshold.numerator * (objective.denominator - objective.numerator) > threshold.denominator * objective.denominator)
      return 'This threshold cannot be reached at the selected SLO target.';
  }
  return undefined;
}
function displayedUnit(seconds: number): number {
  return seconds % 86400 === 0 ? 86400 : seconds % 3600 === 0 ? 3600 : 60;
}
export function formatAlertDuration(seconds: number): string {
  const unit = displayedUnit(seconds);
  return `${seconds / unit} ${unit === 86400 ? 'd' : unit === 3600 ? 'h' : 'min'}`;
}
export function parseAlertDuration(text: string, previousSeconds: number, cadenceSeconds: number): number | null {
  const match = text.trim().match(/^(\d+(?:\.\d+)?)\s*(m|min(?:ute)?s?|h|hours?|hrs?|d|days?)?$/i);
  if (!match || !Number.isSafeInteger(cadenceSeconds) || cadenceSeconds <= 0) return null;
  const amount = decimal(match[1]!)!;
  const name = match[2]?.toLowerCase();
  const unit = name ? name.startsWith('m') ? 60 : name.startsWith('h') ? 3600 : 86400 : displayedUnit(previousSeconds);
  const numerator = amount.numerator * BigInt(unit);
  if (numerator % amount.denominator !== 0n) return null;
  const seconds = numerator / amount.denominator;
  if (seconds <= 0n || seconds > 2592000n || seconds % 60n !== 0n || seconds % BigInt(cadenceSeconds) !== 0n) return null;
  return Number(seconds);
}
export function defaultAlertConditions(): AlertConditionDraft[] {
  return [
    { id: crypto.randomUUID(), shortSeconds: 300, longSeconds: 3600, threshold: '14.4' },
    { id: crypto.randomUUID(), shortSeconds: 1800, longSeconds: 21600, threshold: '6' },
  ];
}
export type AlertConditionPreset = 'fast' | 'sustained' | 'custom';
export function conditionPreset(condition: Pick<AlertConditionDraft, 'shortSeconds' | 'longSeconds' | 'threshold'>): AlertConditionPreset {
  const threshold = normalizeAlertThreshold(condition.threshold);
  if (condition.shortSeconds === 300 && condition.longSeconds === 3600 && threshold === '14.4') return 'fast';
  if (condition.shortSeconds === 1800 && condition.longSeconds === 21600 && threshold === '6') return 'sustained';
  return 'custom';
}
export const conditionLabels = { fast: 'Fast burn', sustained: 'Sustained burn', custom: 'Custom condition' } as const;
export const conditionDescriptions = { fast: 'Catch sudden, severe degradation.', sustained: 'Catch persistent, lower-rate degradation.', custom: 'Your own windows and burn threshold.' } as const;
