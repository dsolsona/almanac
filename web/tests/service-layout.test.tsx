import {expect,it} from 'vitest';
import {networkLayout} from '../src/services/networkLayout.ts';
import {serviceFleet} from '../browser/support/service-fleet.ts';

it('keeps a fifty-service topology stable across API ordering and repeated reads',()=>{
 const records=serviceFleet(),layout=networkLayout(records);
 expect(Object.keys(layout.positions).sort()).toEqual(records.map(record=>record.id!).sort());
 expect(networkLayout([...records].reverse()).positions).toEqual(layout.positions);
 expect(networkLayout(records.map(record=>({...record,dependencies:[...(record.dependencies??[])].reverse()}))).positions).toEqual(layout.positions);
 expect(layout.width/layout.height).toBeGreaterThan(1.2);
 expect(layout.width/layout.height).toBeLessThan(2.5);
 expect(Object.values(layout.labelScales).every(scale=>scale>=1&&scale<=2)).toBe(true);
 for(const [x,y]of Object.values(layout.positions)){expect(Number.isFinite(x)&&Number.isFinite(y)).toBe(true);expect(x).toBeGreaterThan(0);expect(x).toBeLessThan(layout.width);expect(y).toBeGreaterThan(0);expect(y).toBeLessThan(layout.height);}
});

it('handles cycles, disconnected services and unloaded targets within a fixed layout budget',()=>{
 const records=serviceFleet().slice(0,3);
 records[0]!.dependencies=[records[1]!.id!];records[1]!.dependencies=[records[0]!.id!,'00000000-0000-4000-8000-999999999999'];
 const layout=networkLayout(records);
 expect(Object.keys(layout.positions)).toHaveLength(3);
 expect(Object.values(layout.positions).flat().every(Number.isFinite)).toBe(true);
 expect(networkLayout([]).positions).toEqual({});
 expect(Object.keys(networkLayout([records[0]!]).positions)).toEqual([records[0]!.id]);
});

it('packs owners into distinct compact regions while retaining all service IDs',()=>{
 const records=serviceFleet(),layout=networkLayout(records,true);
 expect(Object.keys(layout.positions)).toHaveLength(50);
 expect(layout.groups.map(group=>group.owner).sort()).toEqual([...new Set(records.map(record=>record.owner??''))].sort());
 expect(layout.width/layout.height).toBeGreaterThan(.8);
 for(const [index,a]of layout.groups.entries())for(const b of layout.groups.slice(index+1))expect(Math.abs(a.cx-b.cx)>=a.rx+b.rx||Math.abs(a.cy-b.cy)>=a.ry+b.ry).toBe(true);
});
