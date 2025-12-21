import { Admin } from "@/pages/Admin";
import { AdminLogin } from "@/pages/AdminLogin";
import { ServerDetail } from "@/pages/ServerDetail";
import { ServerList } from "@/pages/ServerList";
import { Route, Routes } from "react-router-dom";

function App() {
  return (
    <Routes>
      <Route path="/" element={<ServerList />} />
      <Route path="/server/:id" element={<ServerDetail />} />
      <Route path="/admin/login" element={<AdminLogin />} />
      <Route path="/admin" element={<Admin />} />
    </Routes>
  );
}

export default App;
