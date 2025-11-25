import { Routes, Route } from "react-router-dom";
import { ServerList } from "@/pages/ServerList";
import { ServerDetail } from "@/pages/ServerDetail";

function App() {
  return (
    <Routes>
      <Route path="/" element={<ServerList />} />
      <Route path="/server/:id" element={<ServerDetail />} />
    </Routes>
  );
}

export default App;
