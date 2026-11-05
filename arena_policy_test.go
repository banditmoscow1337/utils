//go:build linux && (amd64 || arm64 || riscv64)
package utils
import ("testing";"os";"encoding/binary")
func TestArenaClassPolicies(t *testing.T) {
 for _,nonMoving:=range []bool{false,true}{
  a,err:=NewArenaWithOptions(4*RegionSize,-1,ArenaOptions{NonMoving:nonMoving});if err!=nil{t.Fatal(err)}
  p:=a.Alloc(8192);want:=uint64(8192);if nonMoving{want=8256-HeaderSize}
  if got:=a.PayloadCapacity(p);got!=want{t.Fatalf("mode=%v capacity=%d want=%d",nonMoving,got,want)}
  a.Free(p);if err=a.Close();err!=nil{t.Fatal(err)}
 }
}
func TestDefaultArenaRecyclesExactRegionsAndPurges(t *testing.T){
 a,err:=NewArena[uint64](3*RegionSize,-1);if err!=nil{t.Fatal(err)};defer a.Close()
 for i:=0;i<1000;i++{p:=a.AllocBatch(65536-HeaderSize,1);a.Free(p)}
 if err=a.Trim();err!=nil{t.Fatal(err)}
 s:=a.Stats()
 if s.RegionReuses<999||s.PurgeCalls==0||s.ActiveClaims!=0||s.ClaimedBytes!=0{t.Fatalf("reclamation: %+v",s)}
}
func TestEvacuationUsesCurrentRegionOccupancy(t *testing.T){
 a,err:=NewArena[uint64](4*RegionSize,-1);if err!=nil{t.Fatal(err)};defer a.Close()
 old:=a.AllocBatch(RegionSize-HeaderSize,1);a.Free(old)
 p:=a.AllocDense(64);guard:=a.AllocDense(64);a.Free(p)
 if a.NeedsEvacuation(guard){t.Fatal("historical high water evacuated a reused region")};a.Free(guard)
}
func TestArenaFormatsAndCheckedPointer(t *testing.T){
 f,err:=os.CreateTemp(t.TempDir(),"formats");if err!=nil{t.Fatal(err)};defer f.Close()
 const size=4*RegionSize
 if err=f.Truncate(size);err!=nil{t.Fatal(err)}
 a,err:=NewArena[uint64](size,int(f.Fd()));if err!=nil{t.Fatal(err)}
 p:=a.Alloc(64);*(*uint64)(a.GetPtr(p))=0x1122334455667788
 if err=a.Close();err!=nil{t.Fatal(err)}
 if other,err:=NewArenaWithOptions(size,int(f.Fd()),ArenaOptions{NonMoving:true});err==nil{other.Close();t.Fatal("different policy accepted")}
 a,err=NewArena[uint64](size,int(f.Fd()));if err!=nil{t.Fatal(err)}
 if *(*uint64)(a.GetPtr(p))!=0x1122334455667788{t.Fatal("reopen damaged payload")}
 a.Free(p);mustPanic(t,func(){a.CheckedPtr(size)})
 if err=a.Close();err!=nil{t.Fatal(err)};mustPanic(t,func(){a.CheckedPtr(p)})
 var header [24]byte
 binary.LittleEndian.PutUint64(header[0:],4096)
 binary.LittleEndian.PutUint64(header[8:],0x6172656e61363401)
 binary.LittleEndian.PutUint64(header[16:],size)
 if _,err=f.WriteAt(header[:],0);err!=nil{t.Fatal(err)}
 if other,err:=NewArena[uint64](size,int(f.Fd()));err==nil{other.Close();t.Fatal("original format accepted")}
}
