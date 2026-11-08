import './label-chips.css';
export type LabelPair=readonly [string,string];
export function LabelChips({labels,onRemove,disabled=false}:{labels:readonly LabelPair[];onRemove?:(index:number)=>void;disabled?:boolean}){
 return <ul className="ui-label-chips" aria-label={onRemove?'Added labels':'Labels'}>{labels.map(([key,value],index)=><li key={`${key}:${value}`} className="ui-label-chip"><span className="ui-label-chip__key">{key}<span className="ui-label-chip__colon">:</span></span><span className="ui-label-chip__value">{value}</span>{onRemove&&<button type="button" disabled={disabled} onClick={()=>onRemove(index)} aria-label={`Remove label ${key}:${value}`}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"/></svg></button>}</li>)}</ul>;
}
