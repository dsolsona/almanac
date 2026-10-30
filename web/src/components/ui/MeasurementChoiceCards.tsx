import type { ChangeEvent } from 'react';
import './measurement-controls.css';

type MeasurementKind = 'event_counts' | 'fraction' | 'threshold';

type Props = {
  value: MeasurementKind;
  onChange: (value: MeasurementKind) => void;
};

const choices: { value: MeasurementKind; title: string; description: string; availability: string }[] = [
  {
    value: 'event_counts',
    title: 'Ratio · two queries',
    description: 'Good and total event counts. Each event weighs the same; a quiet interval contributes fewer events than a busy one.',
    availability: 'Available now',
  },
  {
    value: 'fraction',
    title: 'Ratio · single fraction',
    description: 'One query returning a fraction from 0 to 1. Each interval weighs the same; event weighting is lost.',
    availability: 'Not available yet. Fraction collection is not supported by the assigned agent.',
  },
  {
    value: 'threshold',
    title: 'Threshold',
    description: 'One value compared with a threshold. Each interval is classified as good or bad.',
    availability: 'Not available yet. Threshold collection is not supported by the assigned agent.',
  },
];

export function MeasurementChoiceCards({ value, onChange }: Props) {
  function select(event: ChangeEvent<HTMLInputElement>) {
    if (event.currentTarget.value === 'event_counts') onChange('event_counts');
  }

  return <fieldset className="measurement-choices">
    <legend>Measurement</legend>
    <div className="measurement-choices__grid">
      {choices.map((choice) => {
        const supported = choice.value === 'event_counts';
        return <label key={choice.value} className={`measurement-choice${value === choice.value ? ' measurement-choice--selected' : ''}${supported ? '' : ' measurement-choice--unavailable'}`}>
          <span className="measurement-choice__heading">
            <input type="radio" name="sli-measurement-kind" value={choice.value} checked={value === choice.value} disabled={!supported} onChange={select} />
            <span>{choice.title}</span>
            {supported && <span className="measurement-choice__recommended">Recommended</span>}
          </span>
          <span className="measurement-choice__description">{choice.description}</span>
          {!supported && <span className="measurement-choice__availability">{choice.availability}</span>}
        </label>;
      })}
    </div>
  </fieldset>;
}
