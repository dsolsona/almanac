import { render, screen } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { ChartBoundary } from '../src/slos/SlosPage.tsx';
it('keeps exact values available if the chart import fails',()=>{
 const failure=vi.spyOn(console,'error').mockImplementation(()=>{});
 function Broken():never {throw new Error('chart import failed');}
 render(<><ChartBoundary><Broken/></ChartBoundary><p>Exact values remain usable</p></>);
 expect(screen.getByRole('alert')).toHaveTextContent('chart could not be opened');expect(screen.getByText('Exact values remain usable')).toBeVisible();failure.mockRestore();
});
