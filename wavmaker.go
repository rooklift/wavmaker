package wavmaker

import (
	"encoding/binary"
	"fmt"
	"os"
)

const PREFERRED_FREQ = 44100

type WAV struct {
	FmtChunk FmtChunk_Struct
	DataChunk DataChunk_Struct
}

type FmtChunk_Struct struct {
	Size uint32
	AudioFormat uint16
	NumChannels uint16
	SampleRate uint32
	ByteRate uint32
	BlockAlign uint16
	BitsPerSample uint16
}

type DataChunk_Struct struct {
	Size uint32
	Data []byte
}

var have_warned_get_out_of_bounds bool = false
var have_warned_set_out_of_bounds bool = false
var have_warned_clipping bool = false


// ------------------------------------- EXPOSED METHODS


func (wav *WAV) FrameCount() uint32 {
	return wav.DataChunk.Size / uint32(wav.FmtChunk.BlockAlign)
}


func (wav *WAV) Copy() *WAV {

	var new_wav WAV

	new_wav.FmtChunk = wav.FmtChunk
	new_wav.DataChunk.Size = wav.DataChunk.Size
	new_wav.DataChunk.Data = make([]byte, len(wav.DataChunk.Data))
	copy(new_wav.DataChunk.Data, wav.DataChunk.Data)

	if wav.sanitycheck() != nil {
		panic("newly copied WAV was not valid")
	}

	return &new_wav
}


func (original *WAV) Stretched(new_frame_count uint32) *WAV {

	// This uses linear interpolation to do the stretching or
	// squashing, which sound techies don't recommend as it's lossy.

	if new_frame_count == original.FrameCount() {
		return original.Copy()
	}

	new_wav := New(new_frame_count)

	if new_frame_count == 0 {
		return new_wav
	}

	// Set the final frame directly...

	left, right := original.Get(original.FrameCount() - 1)
	new_wav.Set(new_frame_count - 1, left, right)

	for n := uint32(0) ; n <= new_frame_count - 2 ; n++ {

		index_f := (float64(n) / float64(new_frame_count - 1)) * float64(original.FrameCount() - 1)
		index := uint32(index_f)

		interpolate_fraction := index_f - float64(index)

		old_val_left,      old_val_right      := original.Get(index)
		old_val_left_next, old_val_right_next := original.Get(index + 1)

		diff_left  := old_val_left_next  - old_val_left
		diff_right := old_val_right_next - old_val_right

		new_val_left_f  := float64(old_val_left)  + float64(diff_left)  * interpolate_fraction
		new_val_right_f := float64(old_val_right) + float64(diff_right) * interpolate_fraction

		new_val_left  := int16(new_val_left_f)
		new_val_right := int16(new_val_right_f)

		new_wav.Set(n, new_val_left, new_val_right)
	}

	return new_wav
}


func (wav *WAV) StretchedRelative(multiplier float64) *WAV {

	old_framecount_f := float64(wav.FrameCount())
	new_framecount_f := old_framecount_f * multiplier

	new_framecount := uint32(new_framecount_f)

	return wav.Stretched(new_framecount)
}


func (wav *WAV) Save(filename string) error {

	outfile, err := os.Create(filename)
	if outfile != nil {
		defer outfile.Close()
	}
    if err != nil {
        return fmt.Errorf("Couldn't create output file '%s'", filename)
	}

	filesize := 36 + wav.DataChunk.Size

	// Conceptually one might think of strings as being big endian, but because
	// they are comprised of byte-sized units, they have no endianness at all.

	bo := binary.LittleEndian

	binary.Write(outfile, bo, []byte("RIFF"))
	binary.Write(outfile, bo, &filesize)
	binary.Write(outfile, bo, []byte("WAVE"))
	binary.Write(outfile, bo, []byte("fmt "))
	binary.Write(outfile, bo, &wav.FmtChunk.Size)
	binary.Write(outfile, bo, &wav.FmtChunk.AudioFormat)
	binary.Write(outfile, bo, &wav.FmtChunk.NumChannels)
	binary.Write(outfile, bo, &wav.FmtChunk.SampleRate)
	binary.Write(outfile, bo, &wav.FmtChunk.ByteRate)
	binary.Write(outfile, bo, &wav.FmtChunk.BlockAlign)
	binary.Write(outfile, bo, &wav.FmtChunk.BitsPerSample)
	binary.Write(outfile, bo, []byte("data"))
	binary.Write(outfile, bo, &wav.DataChunk.Size)
	binary.Write(outfile, bo, wav.DataChunk.Data)

	err = wav.sanitycheck()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: while saving '%s', %v\n", filename, err)
	}

	return nil
}


func (wav *WAV) Set(frame uint32, left, right int16) {

	// Assumes the wav is 16-bit stereo

	if frame >= wav.DataChunk.Size / 4 {
		if have_warned_set_out_of_bounds == false {
			have_warned_set_out_of_bounds = true
			fmt.Fprintf(os.Stderr, "Warning: out of bounds Set(). No further such warnings shall be given.\n")
		}
		return
	}

	n := frame * 4

	// Reminder to self, humans and compilers think in big-endian but the storage is little-endian...

	wav.DataChunk.Data[n + 0] = byte(left & 0xff)		// The less-significant byte
	wav.DataChunk.Data[n + 1] = byte(left >> 8)			// The more-significant byte

	wav.DataChunk.Data[n + 2] = byte(right & 0xff)		// The less-significant byte
	wav.DataChunk.Data[n + 3] = byte(right >> 8)		// The more-significant byte
}


func (wav *WAV) Get(frame uint32) (int16, int16) {

	// Assumes the wav is 16-bit stereo

	if frame >= wav.DataChunk.Size / 4 {
		if have_warned_get_out_of_bounds == false {
			have_warned_get_out_of_bounds = true
			fmt.Fprintf(os.Stderr, "Warning: out of bounds Get(). No further such warnings shall be given.\n")
		}
		return 0, 0
	}

	n := frame * 4

	left  := int16(wav.DataChunk.Data[n + 0]) | (int16(wav.DataChunk.Data[n + 1]) << 8)
	right := int16(wav.DataChunk.Data[n + 2]) | (int16(wav.DataChunk.Data[n + 3]) << 8)

	return left, right
}


func (target *WAV) Add(t_loc uint32, source *WAV, s_loc uint32, frames uint32, volume float64, fadeout uint32) {

	// This function adds the source wav to the target, with various options. It is highly relevant to my related
	// Trackmaker project, and indeed perhaps includes too much logic specific to that. If things get out of hand,
	// it should just be moved into that project, and a simplified feature-reduced version placed here.

	t := t_loc
	s := s_loc
	frames_added := uint32(0)

	var clipped bool = false

	for {
		if t >= target.FrameCount() {
			break
		}
		if s >= source.FrameCount() {
			break
		}

		target_left, target_right := target.Get(t)
		source_left, source_right := source.Get(s)

		frames_to_go := frames - frames_added
		if frames_to_go < fadeout {
			fade_multiplier := float64(frames_to_go) / float64(fadeout)

			source_left  = int16(fade_multiplier * float64(source_left))
			source_right = int16(fade_multiplier * float64(source_right))
		}

		var new_left_32, new_right_32 int32

		if volume == 1.0 {
			new_left_32  = int32(target_left)  + int32(source_left)
			new_right_32 = int32(target_right) + int32(source_right)
		} else {
			new_left_32  = int32(target_left)  + int32(float64(source_left) * volume)
			new_right_32 = int32(target_right) + int32(float64(source_right) * volume)
		}

		if new_left_32  < -32768 { new_left_32  = -32768 ; clipped = true }
		if new_left_32  >  32767 { new_left_32  =  32767 ; clipped = true }
		if new_right_32 < -32768 { new_right_32 = -32768 ; clipped = true }
		if new_right_32 >  32767 { new_right_32 =  32767 ; clipped = true }

		new_left  := int16(new_left_32)
		new_right := int16(new_right_32)

		target.Set(t, new_left, new_right)

		t++
		s++

		frames_added++
		if frames_added >= frames {
			break
		}
	}

	if clipped == true && have_warned_clipping == false {
		have_warned_clipping = true
		fmt.Fprintf(os.Stderr, "Warning: clipping occurred in Add(). No further such warnings shall be given.\n")
	}
}


func (wav *WAV) FadeSamples(frames_to_fade uint32) {

	if frames_to_fade <= 0 {
		return
	}

	total_frames := wav.FrameCount()

	if total_frames < 2 {
		return
	}

	if frames_to_fade > total_frames {
		frames_to_fade = total_frames
	}

	for n := total_frames - 1 ; n > total_frames - frames_to_fade ; n-- {	// Use > not >= because of uint wrap-around

		multiplier := float64(total_frames - n) / float64(frames_to_fade)

		old_left, old_right := wav.Get(n)

		new_left_f  := float64(old_left)  * multiplier
		new_right_f := float64(old_right) * multiplier

		new_left  := int16(new_left_f)
		new_right := int16(new_right_f)

		wav.Set(n, new_left, new_right)
	}
}


func (wav *WAV) FadeFraction(fraction float64) {		// e.g. an argument of 0.25 fades out the final 25%

	if fraction <= 0 {
		return
	}
	if fraction > 1 {
		fraction = 1
	}

	total_frames := wav.FrameCount()
	frames_to_fade := uint32(float64(total_frames) * fraction)

	wav.FadeSamples(frames_to_fade)
}


// ------------------------------------- EXPOSED FUNCTIONS


func Load(filename string) (*WAV, error) {

	var infile *os.File
	var err error
	var buf [4]byte
	var wav WAV
	var got_fmt, got_data bool

	infile, err = os.Open(filename)
	if infile != nil {
		defer infile.Close()
	}
	if err != nil {
		return &wav, fmt.Errorf("load_wav() couldn't load '%s': %v", filename, err)
	}

	// --------------------

	err = binary.Read(infile, binary.LittleEndian, &buf)
	if err != nil {
		return &wav, fmt.Errorf("load_wav() couldn't read RIFF bytes: %v", err)
	}
	if buf != [4]byte{'R', 'I', 'F', 'F'} {
		return &wav, fmt.Errorf("load_wav() found bytes 0-3 != RIFF")
	}

	// --------------------

	var totalsize uint32

	err = binary.Read(infile, binary.LittleEndian, &totalsize)
	if err != nil {
		return &wav, fmt.Errorf("load_wav() couldn't read total file size: %v", err)
	}

	// --------------------

	err = binary.Read(infile, binary.LittleEndian, &buf)
	if err != nil {
		return &wav, fmt.Errorf("load_wav() couldn't read WAVE bytes: %v", err)
	}
	if buf != [4]byte{'W', 'A', 'V', 'E'} {
		return &wav, fmt.Errorf("load_wav() found bytes 8-11 != WAVE")
	}

	// --------------------

	for {

		err = binary.Read(infile, binary.LittleEndian, &buf)
		if err != nil {
			return &wav, fmt.Errorf("load_wav() couldn't read chunk's starting bytes: %v", err)
		}

		if buf == [4]byte{'f', 'm', 't', ' '} {
			wav.FmtChunk, err = load_fmt(infile)
			if err != nil {
				return &wav, err
			}
			got_fmt = true
		} else if buf == [4]byte{'d', 'a', 't', 'a'} {
			wav.DataChunk, err = load_data(infile)
			if err != nil {
				return &wav, err
			}
			got_data = true
		} else {
			err = skip_chunk(infile, buf)
			if err != nil {
				return &wav, err
			}
		}

		if got_fmt && got_data {
			break
		}
	}

	// --------------------

	err = wav.sanitycheck()
	if err != nil {
		return &wav, err
	}

	err = wav.convert(filename)
	if err != nil {
		return &wav, err
	}

	return &wav, nil
}


func New(frames uint32) *WAV {

	var wav WAV

	wav.FmtChunk.Size = 16
	wav.FmtChunk.AudioFormat = 1
	wav.FmtChunk.NumChannels = 2
	wav.FmtChunk.SampleRate = PREFERRED_FREQ
	wav.FmtChunk.ByteRate = PREFERRED_FREQ * 4		// Bytes per second; we are using 4 bytes per frame
	wav.FmtChunk.BlockAlign = 4
	wav.FmtChunk.BitsPerSample = 16

	wav.DataChunk.Size = uint32(wav.FmtChunk.BitsPerSample / 8) * frames * uint32(wav.FmtChunk.NumChannels)
	wav.DataChunk.Data = make([]byte, wav.DataChunk.Size)

	if wav.sanitycheck() != nil {
		panic("failed to create a valid WAV")
	}

	return &wav
}


// ------------------------------------- NON-EXPOSED FUNCTIONS


func skip_chunk(infile *os.File, chunk_name [4]byte) error {

	var chunk_size uint32
	var err error
	var buf byte

	err = binary.Read(infile, binary.LittleEndian, &chunk_size)
	if err != nil {
		return fmt.Errorf("skip_chunk() couldn't read '%s' chunk size: %v", chunk_name, err)
	}

	for n := uint32(0) ; n < chunk_size ; n++ {
		err = binary.Read(infile, binary.LittleEndian, &buf)
		if err != nil {
			return fmt.Errorf("skip_chunk() couldn't read '%s' chunk contents: %v", chunk_name, err)
		}
	}

	return nil
}


func load_fmt(infile *os.File) (FmtChunk_Struct, error) {

	var chunk FmtChunk_Struct
	var err error

	err = binary.Read(infile, binary.LittleEndian, &chunk)
	if err != nil {
		return chunk, fmt.Errorf("load_fmt() couldn't read fmt chunk: %v", err)
	}

	return chunk, nil
}


func load_data(infile *os.File) (DataChunk_Struct, error) {

	var chunk DataChunk_Struct
	var err error

	binary.Read(infile, binary.LittleEndian, &chunk.Size)
	err = binary.Read(infile, binary.LittleEndian, chunk.Data)
	if err != nil {
		return chunk, fmt.Errorf("load_data() couldn't read chunk size: %v", err)
	}

	chunk.Data = make([]byte, chunk.Size)

	err = binary.Read(infile, binary.LittleEndian, chunk.Data)
	if err != nil {
		return chunk, fmt.Errorf("load_data() couldn't read data: %v", err)
	}

	return chunk, nil
}


// ------------------------------------- NON-EXPOSED METHODS


func (wav *WAV) convert(filename string) error {		// Filename given just for printing useful info

	// Remember, this is an in-place conversion, we can't just set *wav ptr to be something else.
	// Rather, the struct that *wav points to itself needs to be modified.

	// We want 16-bit audio:

	if wav.FmtChunk.BitsPerSample != 16 {

		if wav.FmtChunk.BitsPerSample != 8 {
			return fmt.Errorf("convert_wav(): bits per sample in '%s' was not 8 or 16", filename)
		}

		fmt.Fprintf(os.Stderr, "Converting '%s' to 16 bit...\n", filename)

		new_data := make([]byte, wav.DataChunk.Size * 2)

		for n := uint32(0) ; n < wav.DataChunk.Size ; n++ {

			old_val := int32(wav.DataChunk.Data[n])

			new_val := ((old_val - 128) * 256) + old_val

			// Reminder to self, humans and compilers think in big-endian but the storage is little-endian...

			new_data[n * 2] = byte(new_val & 0xff)			// The less-significant bytes
			new_data[n * 2 + 1] = byte(new_val >> 8)		// The more-significant bytes
		}

		wav.FmtChunk.BitsPerSample = 16

		wav.FmtChunk.ByteRate *= 2
		wav.FmtChunk.BlockAlign *= 2

		wav.DataChunk.Data = new_data
		wav.DataChunk.Size *= 2
	}

	// We want stereo:

	if wav.FmtChunk.NumChannels == 1 {

		fmt.Fprintf(os.Stderr, "Converting '%s' to stereo...\n", filename)

		new_data := make([]byte, wav.DataChunk.Size * 2)

		for n := uint32(0) ; n < wav.DataChunk.Size ; n += 2 {

			// Things are guaranteed 16-bit at this point, so the following is right...

			new_data[n * 2] = wav.DataChunk.Data[n]
			new_data[n * 2 + 2] = wav.DataChunk.Data[n]

			new_data[n * 2 + 1] = wav.DataChunk.Data[n + 1]
			new_data[n * 2 + 3] = wav.DataChunk.Data[n + 1]
		}

		wav.FmtChunk.NumChannels = 2

		wav.FmtChunk.ByteRate *= 2
		wav.FmtChunk.BlockAlign *= 2

		wav.DataChunk.Data = new_data
		wav.DataChunk.Size *= 2
	}

	// We want 44100 Hz:

	if wav.FmtChunk.SampleRate != PREFERRED_FREQ {

		new_frame_count := wav.FrameCount() * PREFERRED_FREQ / wav.FmtChunk.SampleRate
		fmt.Fprintf(os.Stderr, "Converting '%s' to %d Hz ", filename, PREFERRED_FREQ)
		fmt.Fprintf(os.Stderr, " (%d -> %d frames)...\n", wav.FrameCount(), new_frame_count)
		*wav = *wav.Stretched(new_frame_count)
	}

	// Final sanity check:

	err := wav.sanitycheck()
	if err != nil {
		return fmt.Errorf("convert_wav(): seemed to succeed, but: %v", err)
	}

	return nil
}


func (wav *WAV) sanitycheck() error {

	if wav.FmtChunk.Size != 16 {
		return fmt.Errorf("sanitycheck(): fmt chunk size != 16")
	}

	if wav.FmtChunk.AudioFormat != 1 {
		return fmt.Errorf("sanitycheck(): audio format != 1 (PCM)")
	}

	if wav.FmtChunk.NumChannels > 2 {
		return fmt.Errorf("sanitycheck(): num channels > 2")
	}

	if wav.FmtChunk.ByteRate != wav.FmtChunk.SampleRate * uint32(wav.FmtChunk.NumChannels) * uint32(wav.FmtChunk.BitsPerSample) / 8 {
		return fmt.Errorf("sanitycheck(): byte rate did not match other fmt fields")
	}

	if wav.FmtChunk.BlockAlign != wav.FmtChunk.NumChannels * wav.FmtChunk.BitsPerSample / 8 {
		return fmt.Errorf("sanitycheck(): block align did not match other fmt fields")
	}

	if wav.DataChunk.Size != uint32(len(wav.DataChunk.Data)) {
		return fmt.Errorf("sanitycheck(): data chunk size did not match amount of data read")
	}

	return nil
}
