import { fireEvent, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { expect, test, vi } from 'vitest';
import AdminsPage from '@/pages/delegation/AdminsPage';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

vi.mock('@/layouts/AppSidebar', () => ({ default: () => null }));
vi.mock('@/routes/panelAccess', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/routes/panelAccess')>(),
  usePanelAccount: () => ({ data: { id: 1, role: 'admin', clients: 10, gib: 10 } }),
  readDelegation: async () => [
    { id: 2, parentId: 1, username: 'existing-shop', role: 'reseller', clients: 0, gib: 0 },
  ],
}));

test('shows username conflicts inline and displays a toast when deletion fails', async () => {
  const post = vi.spyOn(HttpUtil, 'post');
  post.mockResolvedValueOnce(new Msg(false, 'username_taken'));
  post.mockResolvedValueOnce(new Msg(false, 'request_failed'));

  try {
    renderWithProviders(<MemoryRouter><AdminsPage /></MemoryRouter>);
    await screen.findByText('existing-shop');
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByText('This username is already in use.')).toBeTruthy();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }));

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    fireEvent.click(await screen.findByRole('button', { name: 'OK' }));
    expect(await screen.findByText('Account deletion could not be completed. Check the list and try again.')).toBeTruthy();
    expect(screen.getByRole('row', { name: /existing-shop/ })).toBeTruthy();
  } finally {
    post.mockRestore();
  }
});
