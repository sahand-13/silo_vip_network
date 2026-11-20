import { Navigate, Outlet, useLocation } from 'react-router';
import { Alert, Spin } from 'antd';
import { useTranslation } from 'react-i18next';

import { useWebSocketBridge } from '@/api/websocketBridge';
import { usePageTitle } from '@/hooks/usePageTitle';
import CommandPalette from '@/components/command-palette/CommandPalette';
import { PanelRoleContext, panelRouteAllowed, usePanelAccount } from '@/routes/panelAccess';

function OwnerTools() {
  useWebSocketBridge();
  return <CommandPalette />;
}

export default function PanelLayout() {
  const { t } = useTranslation();
  const { pathname } = useLocation();
  const account = usePanelAccount();
  usePageTitle();
  if (account.isPending) return <Spin fullscreen />;
  if (!account.data) return <Alert type="error" message={t('delegation.request_failed')} />;
  const { role } = account.data;
  const path = pathname.replace(/\/+$/, '') || '/';
  if (!panelRouteAllowed(path, role)) {
    return <Navigate to={role === 'owner' ? '/' : '/my-clients'} replace />;
  }
  return (
    <PanelRoleContext.Provider value={role}>
      <Outlet />
      {role === 'owner' && <OwnerTools />}
    </PanelRoleContext.Provider>
  );
}
