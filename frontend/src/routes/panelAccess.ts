import { createContext, useContext } from 'react';
import { useQuery } from '@tanstack/react-query';
import { HttpUtil } from '@/utils';

export type PanelRole = 'owner' | 'sudo' | 'admin' | 'reseller' | 'infinity';
export type PanelAccount = {
  id: number; username: string; role: PanelRole; clients: number; gib: number; parentId: number;
  uncappedClientSlots?: boolean; unlimitedClientLimit?: number; unlimitedClientAvailable?: number;
};
export const delegationPath = '/panel/api/delegation';
export const panelAccountKey = ['panel-account'] as const;
export const PanelRoleContext = createContext<PanelRole>('owner');
export const usePanelRole = () => useContext(PanelRoleContext);

export async function readDelegation<T>(path: string): Promise<T> {
  const msg = await HttpUtil.get<T>(delegationPath + path, undefined, { silent: true });
  if (!msg.success || msg.obj == null) throw new Error(msg.msg);
  return msg.obj;
}

export function usePanelAccount() {
  return useQuery({
    queryKey: panelAccountKey,
    queryFn: () => readDelegation<PanelAccount>('/me'),
    retry: false,
  });
}

export function panelRouteAllowed(path: string, role: PanelRole): boolean {
  if (role === 'owner') return path !== '/my-clients' && path !== '/my-backup' && path !== '/xray-overview';
  if (path === '/my-clients' || path === '/my-backup') return true;
  if (path === '/admins') return role === 'sudo' || role === 'admin' || role === 'infinity';
  return path === '/xray-overview' && (role === 'sudo' || role === 'infinity');
}
