import { fireEvent, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import ClientExpiryPresets from '@/pages/clients/ClientExpiryPresets';
import { renderWithProviders } from './test-utils';

afterEach(() => vi.useRealTimers());

it('offers six calendar-month presets and clamps a month-end expiry', () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(2027, 0, 31, 12));
  const select = vi.fn();
  renderWithProviders(<ClientExpiryPresets delayed={false} onSelect={select} />);
  expect(screen.getAllByRole('button')).toHaveLength(6);
  fireEvent.click(screen.getByRole('button', { name: '1 month' }));
  expect(select).toHaveBeenCalledWith(new Date(2027, 1, 28, 12).valueOf());
});

it('uses a duration in days when a client starts on first use', () => {
  const select = vi.fn();
  renderWithProviders(<ClientExpiryPresets delayed onSelect={select} />);
  fireEvent.click(screen.getByRole('button', { name: '6 months' }));
  expect(select).toHaveBeenCalledWith(180);
});
