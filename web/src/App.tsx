import { useState } from "react";
import AppLayout from "@cloudscape-design/components/app-layout";
import SideNavigation, {
  SideNavigationProps,
} from "@cloudscape-design/components/side-navigation";
import { useClusterState } from "./api";
import Overview from "./pages/Overview";
import Workloads from "./pages/Workloads";
import Nodes from "./pages/Nodes";

type Page = "overview" | "workloads" | "nodes";

const NAV_ITEMS: SideNavigationProps.Item[] = [
  { type: "link", text: "Overview", href: "#overview" },
  { type: "link", text: "Workloads", href: "#workloads" },
  { type: "link", text: "Nodes", href: "#nodes" },
];

export default function App() {
  const [activePage, setActivePage] = useState<Page>("overview");
  const { state, error, loading, refetch } = useClusterState();

  const sharedProps = { state, error, loading, refetch };

  const content = {
    overview: <Overview {...sharedProps} />,
    workloads: <Workloads {...sharedProps} />,
    nodes: <Nodes {...sharedProps} />,
  }[activePage];

  return (
    <AppLayout
      navigation={
        <SideNavigation
          header={{ text: "Orchestrator", href: "#overview" }}
          items={NAV_ITEMS}
          activeHref={`#${activePage}`}
          onFollow={(e) => {
            e.preventDefault();
            setActivePage(e.detail.href.slice(1) as Page);
          }}
        />
      }
      content={content}
      toolsHide
    />
  );
}
